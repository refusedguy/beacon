// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements beacon_get_result (WORKER-PROMPT §4.1): an additive tool
// that hands out a Watch result and stamps readAt on the caller's obligation.
// It never returns full logs or check output, only names, conclusions, links and
// the flaky/retry facts needed to decide the next step (WORKER-PROMPT §4.1,
// ADR 0012). The tool is additive, not read-only: issuing content is a state
// change even though the response is a projection of the Watch.
package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// GetResultInput is the argument object of beacon_get_result. includeChecks is
// accepted for input-contract compatibility; full checks and logs are never
// emitted, so it does not change the response shape.
type GetResultInput struct {
	WatchID       string `json:"watchID" jsonschema:"identifier of the Watch to read"`
	IncludeChecks *bool  `json:"includeChecks,omitempty" jsonschema:"accepted for compatibility; full checks and logs are never returned"`
}

// failureConclusions are the check-level conclusions that make a check a failed
// check. SKIPPED, NEUTRAL and the pending states (WAITING, QUEUED,
// ACTION_REQUIRED) are deliberately absent: WORKER-PROMPT §4.6 allows them.
var failureConclusions = map[string]bool{
	"FAILURE":         true,
	"ERROR":           true,
	"TIMED_OUT":       true,
	"STARTUP_FAILURE": true,
}

// resultStoreReader is the narrow read port used by beacon_get_result and
// beacon_ack. It is satisfied by the store and by test doubles. The core only
// reads; every write goes through ObligationStamper.
type resultStoreReader interface {
	Watch(id string) (domain.Watch, error)
	Obligation(id string) (domain.Obligation, error)
}

// resultTxWriter is the single-writer port used to stamp a delivery fact. The
// production store satisfies it with one SQLite transaction per stamp.
type resultTxWriter interface {
	Write(ctx context.Context, fn func(*sql.Tx) error) error
}

// obligationStamp selects which delivery fact a stamp writes.
type obligationStamp int

const (
	stampRead obligationStamp = iota
	stampClaimed
)

// ObligationStamper writes one delivery fact on an obligation atomically. The
// boolean reports whether the fact was newly written; a false value with a nil
// error means the fact was already present, which is the idempotent repeat.
// ADR 0014 and WORKER-PROMPT §4.3 require read and claim to stay distinct.
type ObligationStamper interface {
	StampRead(ctx context.Context, id string, at time.Time) (domain.Obligation, bool, error)
	StampClaimed(ctx context.Context, id string, at time.Time) (domain.Obligation, bool, error)
}

// SQLObligationStamper implements ObligationStamper against the single-writer
// store. It reads and rewrites the obligation payload inside one transaction,
// so a client is only answered after the fact reached disk (ADR 0015).
type SQLObligationStamper struct {
	w resultTxWriter
}

// NewSQLObligationStamper builds a stamper over the store writer.
func NewSQLObligationStamper(w resultTxWriter) *SQLObligationStamper {
	return &SQLObligationStamper{w: w}
}

// StampRead writes readAt when it is still zero.
func (s *SQLObligationStamper) StampRead(ctx context.Context, id string, at time.Time) (domain.Obligation, bool, error) {
	return s.stamp(ctx, id, stampRead, at)
}

// StampClaimed writes claimedAt when it is still zero.
func (s *SQLObligationStamper) StampClaimed(ctx context.Context, id string, at time.Time) (domain.Obligation, bool, error) {
	return s.stamp(ctx, id, stampClaimed, at)
}

func (s *SQLObligationStamper) stamp(ctx context.Context, id string, field obligationStamp, at time.Time) (domain.Obligation, bool, error) {
	if s == nil || s.w == nil {
		return domain.Obligation{}, false, domain.NewAppError(domain.CodePersistFailed, "result: store writer is nil")
	}
	if id == "" {
		return domain.Obligation{}, false, domain.NewAppError(domain.CodeInvalidInput, "result: obligationID is required")
	}

	out := domain.Obligation{}
	applied := false
	err := s.w.Write(ctx, func(tx *sql.Tx) error {
		var payload []byte
		if err := tx.QueryRow(`SELECT payload FROM obligations WHERE id = ?`, id).Scan(&payload); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.NewAppError(domain.CodeWatchNotFound, "result: obligation not found")
			}
			return err
		}
		var o domain.Obligation
		if err := json.Unmarshal(payload, &o); err != nil {
			return err
		}
		switch field {
		case stampRead:
			if !o.ReadAt.IsZero() {
				out = o
				return nil
			}
			o.ReadAt = at
		case stampClaimed:
			if !o.ClaimedAt.IsZero() {
				out = o
				return nil
			}
			o.ClaimedAt = at
		}
		next, err := json.Marshal(o)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE obligations SET payload = ? WHERE id = ?`, next, id); err != nil {
			return err
		}
		out = o
		applied = true
		return nil
	})
	if err != nil {
		var appErr *domain.AppError
		if errors.As(err, &appErr) {
			return out, applied, err
		}
		return out, applied, domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	return out, applied, nil
}

// GetResultHandler implements beacon_get_result over the read port and the
// transactional stamper. One handler owns a clock so repeated reads observe a
// single, caller-controlled notion of "now".
type GetResultHandler struct {
	store   resultStoreReader
	stamper ObligationStamper
	clock   domain.Clock
	after   time.Duration
}

// NewGetResultHandler builds a handler. A nil clock falls back to wall time.
func NewGetResultHandler(store resultStoreReader, stamper ObligationStamper, clock domain.Clock) *GetResultHandler {
	if clock == nil {
		clock = getResultWallClock{}
	}
	return &GetResultHandler{store: store, stamper: stamper, clock: clock}
}

// SetAfter sets the MCP-boundary threshold that turns a slow call into the
// dialect's task/job form. Zero keeps the classical response.
func (h *GetResultHandler) SetAfter(after time.Duration) {
	if h != nil {
		h.after = after
	}
}

type getResultWallClock struct{}

func (getResultWallClock) Now() time.Time { return time.Now() }

// Handle adapts Result to the go-sdk typed tool handler signature. Domain
// errors are returned as-is for ToErrorShape to convert at the MCP boundary.
func (h *GetResultHandler) Handle(ctx context.Context, req *mcpsdk.CallToolRequest, in GetResultInput) (*mcpsdk.CallToolResult, GetResultOutput, error) {
	start := time.Time{}
	if h != nil {
		start = h.clock.Now()
	}
	out, err := h.Result(ctx, req, in)
	if err != nil {
		return nil, GetResultOutput{}, err
	}
	// The core projection above is version-blind. Only this boundary layer
	// chooses the wire form, using the client's capabilities and the --after
	// threshold (ADR 0031 §2, ADR 0003).
	if h != nil {
		out = ApplyTaskForm(out, DialectFromRequest(req), h.clock.Now().Sub(start), h.after)
	}
	return nil, out, nil
}

// Result reads the Watch, finds the caller's obligation at the current
// revision, stamps readAt, and projects the contract fields. It works for any
// verdict, including ready (WORKER-PROMPT §4.1). The caller is identified by its
// opaque sessionID from _meta; the core never interprets that value.
func (h *GetResultHandler) Result(ctx context.Context, req *mcpsdk.CallToolRequest, in GetResultInput) (GetResultOutput, error) {
	if in.WatchID == "" {
		return GetResultOutput{}, domain.NewAppError(domain.CodeInvalidInput, "get_result: watchID is required")
	}
	if h == nil || h.store == nil || h.stamper == nil {
		return GetResultOutput{}, domain.NewAppError(domain.CodePersistFailed, "get_result: store or stamper is nil")
	}

	w, err := h.store.Watch(in.WatchID)
	if err != nil {
		return GetResultOutput{}, mapResultReadError(err, "get_result: watch")
	}

	caller := SessionIDFromRequest(req)
	if caller == nil || *caller == "" {
		return GetResultOutput{}, domain.NewAppError(domain.CodeInvalidInput, "get_result: sessionID from _meta is required")
	}
	obligationID := domain.ObligationKey(w.ID, w.Revision, *caller)
	if _, err := h.store.Obligation(obligationID); err != nil {
		return GetResultOutput{}, mapResultReadError(err, "get_result: obligation")
	}

	if _, _, err := h.stamper.StampRead(ctx, obligationID, h.clock.Now()); err != nil {
		return GetResultOutput{}, err
	}

	failed := collectFailedChecks(w)
	return GetResultOutput{
		Status:       string(w.Status),
		Verdict:      string(w.Verdict),
		Revision:     w.Revision,
		Merge:        MergeSnapshot{Mergeable: w.Merge.Mergeable, MergeStateStatus: w.Merge.MergeStateStatus},
		FailedChecks: failed,
		ObligationID: obligationID,
		RetryBudget:  remainingRetryBudget(w, failed),
	}, nil
}

// collectFailedChecks projects only the fields the contract allows. No check
// output, log text or raw payload is copied into the response.
func collectFailedChecks(w domain.Watch) []FailedCheck {
	out := make([]FailedCheck, 0, len(w.Checks))
	for name, check := range w.Checks {
		if !isFailedCheck(check) {
			continue
		}
		out = append(out, FailedCheck{
			Name:         name,
			Conclusion:   check.Conclusion,
			DetailsURL:   check.DetailsURL,
			Required:     check.Required,
			FlakyMatched: flakyMatched(name, w.Flaky.Names),
		})
	}
	return out
}

func isFailedCheck(c domain.Check) bool {
	if failureConclusions[c.Conclusion] {
		return true
	}
	switch c.State {
	case "FAILURE", "ERROR", "TIMED_OUT", "STARTUP_FAILURE":
		return true
	}
	return false
}

// flakyMatched reports whether name is one of the configured flaky check names
// or matches one of its glob patterns (WORKER-PROMPT §4.6).
func flakyMatched(name string, names []string) bool {
	for _, pattern := range names {
		if pattern == name {
			return true
		}
		if ok, err := path.Match(pattern, name); err == nil && ok {
			return true
		}
	}
	return false
}

// remainingRetryBudget is the smallest retry budget still available across the
// failed checks that matched the flaky set. A retry only happens when every
// failed check is within budget, so the minimum is the binding constraint. A
// watch with no flaky-matched failure has no retry budget.
func remainingRetryBudget(w domain.Watch, failed []FailedCheck) int {
	if len(failed) == 0 || w.Flaky.MaxRetries <= 0 {
		return 0
	}
	budget := 0
	set := false
	for _, fc := range failed {
		if !fc.FlakyMatched {
			continue
		}
		remaining := w.Flaky.MaxRetries - w.Flaky.Used[fc.Name]
		if remaining < 0 {
			remaining = 0
		}
		if !set || remaining < budget {
			budget = remaining
			set = true
		}
	}
	if !set {
		return 0
	}
	return budget
}

// mapResultReadError keeps a domain error unchanged and maps a store read
// failure to the contract code. The narrow read port cannot distinguish a
// missing row from an I/O failure, so a missing Watch or obligation is the
// documented interpretation of a read error here.
func mapResultReadError(err error, what string) error {
	var appErr *domain.AppError
	if errors.As(err, &appErr) {
		return err
	}
	return domain.NewAppError(domain.CodeWatchNotFound, what+": "+err.Error())
}

// --- Result rubric, separate from run completion (ADR 0034 §1) -------------
//
// A Watch reaching StatusClosed only says the run ended. Anthropic reports that
// a run can finish `completed` while the work on its streams failed, so run
// completion and result grading are two different states. The grader below is
// a pure function over Watch facts: it never sees the agent's own reasoning and
// performs no delivery stamp. Its ReadyVerdict is the only verdict allowed to
// mark a task done. The existing beacon_get_result projection is untouched.

// ResultOutcome is the grader's verdict on the work. The five values mirror the
// outcome-evaluation event from Anthropic, adopted in ADR 0034 §1.
type ResultOutcome string

const (
	OutcomeSatisfied            ResultOutcome = "satisfied"
	OutcomeNeedsRevision        ResultOutcome = "needs_revision"
	OutcomeMaxIterationsReached ResultOutcome = "max_iterations_reached"
	OutcomeFailed               ResultOutcome = "failed"
	OutcomeInterrupted          ResultOutcome = "interrupted"
	// OutcomeIndeterminate is the honest task result when an external action
	// was performed but its answer never came back (ADR 0039 §5). The effect
	// may already exist, so the core claims neither success nor failure and
	// starts no retry. It is the outcome of the task itself, not the delivery
	// ladder branch of the same name in ADR 0017 (domain.OutcomeIndeterminate).
	OutcomeIndeterminate ResultOutcome = "indeterminate"
)

// CriterionID names one verifiable criterion. Criteria are fixed before the
// run starts; forming them after the run would fit the rubric to the answer.
type CriterionID string

const (
	// CriterionRunFinished is the run-lifecycle fact, not a proof of work done.
	CriterionRunFinished CriterionID = "run_finished"
	// CriterionRequiredChecksOK fails when a required check has a failed
	// conclusion or failure state.
	CriterionRequiredChecksOK CriterionID = "required_checks_ok"
	// CriterionMergeReady fails on unresolved merge blockers.
	CriterionMergeReady CriterionID = "merge_ready"
	// CriterionVerdictReady checks the derived verdict computed by the domain
	// verdict engine, an observable fact rather than the agent's claim.
	CriterionVerdictReady CriterionID = "verdict_ready"
	// CriterionExternalActionResponse fails when an external action ran but
	// its answer never returned (ADR 0039 §5). The effect is then unknown, so
	// no other criterion can be claimed as proof of success or failure.
	CriterionExternalActionResponse CriterionID = "external_action_response"
)

const (
	defaultOutcomeIterations = 3
	maxOutcomeIterations     = 20
)

// CriterionEvaluation is one criterion result with an explanation. Explanations
// are returned on every outcome so the orchestrator can hand them to the agent
// on the next iteration (ADR 0034 §1).
type CriterionEvaluation struct {
	Criterion   CriterionID `json:"criterion"`
	Passed      bool        `json:"passed"`
	Explanation string      `json:"explanation"`
}

// ResultRubric is the set of verifiable criteria fixed before the run and the
// iteration budget. Criteria are IDs, never free-form claims, so the grader can
// run in its own context over the same facts.
type ResultRubric struct {
	Criteria      []CriterionID `json:"criteria"`
	MaxIterations int           `json:"maxIterations"`
}

// DefaultResultRubric returns the four built-in criteria and three iterations,
// the default budget from ADR 0034 §1.
func DefaultResultRubric() ResultRubric {
	return ResultRubric{
		Criteria: []CriterionID{
			CriterionRunFinished,
			CriterionRequiredChecksOK,
			CriterionMergeReady,
			CriterionVerdictReady,
		},
		MaxIterations: defaultOutcomeIterations,
	}
}

// RubricEvaluation is the separate result record stored next to the terminal
// verdict. ReadyVerdict is set only when Outcome is satisfied: a finished run
// alone never makes a verdict ready.
type RubricEvaluation struct {
	Outcome       ResultOutcome         `json:"outcome"`
	Iteration     int                   `json:"iteration"`
	MaxIterations int                   `json:"maxIterations"`
	Criteria      []CriterionEvaluation `json:"criteria"`
	ReadyVerdict  domain.Verdict        `json:"readyVerdict,omitempty"`
	// LeaseKey is the durable obligation key of the caller that owns an
	// external action whose response was lost (ADR 0039 §5). It is set only for
	// an indeterminate result so the caller can correlate the possible effect
	// without starting a retry. Other outcomes leave it empty and keep their
	// previous bytes.
	LeaseKey string `json:"leaseKey,omitempty"`
}

// gradeResult grades w against rubric. It reads no clock, does no I/O and never
// mutates its arguments, which makes the grader independent of the agent and
// verifiable in CI. iteration is the number of evaluations already performed;
// interrupted reports a caller-driven preemption, which the run cannot claim
// as its own end_turn.
func gradeResult(w domain.Watch, rubric ResultRubric, iteration int, interrupted bool) RubricEvaluation {
	maxIterations := rubric.MaxIterations
	if maxIterations <= 0 {
		maxIterations = defaultOutcomeIterations
	}
	if maxIterations > maxOutcomeIterations {
		maxIterations = maxOutcomeIterations
	}
	if iteration < 0 {
		iteration = 0
	}

	ev := RubricEvaluation{
		Iteration:     iteration,
		MaxIterations: maxIterations,
		Criteria:      make([]CriterionEvaluation, 0, len(rubric.Criteria)),
	}
	allPassed := len(rubric.Criteria) > 0
	runFinished := false
	for _, id := range rubric.Criteria {
		ce := evaluateCriterion(w, id)
		if id == CriterionRunFinished && ce.Passed {
			runFinished = true
		}
		if !ce.Passed {
			allPassed = false
		}
		ev.Criteria = append(ev.Criteria, ce)
	}

	switch {
	case interrupted:
		ev.Outcome = OutcomeInterrupted
	case allPassed:
		ev.Outcome = OutcomeSatisfied
		ev.ReadyVerdict = w.Verdict
	case iteration >= maxIterations:
		ev.Outcome = OutcomeMaxIterationsReached
	case runFinished:
		// The run ended yet at least one criterion failed: completed is not
		// the same as done, and there is no run left to revise.
		ev.Outcome = OutcomeFailed
	default:
		ev.Outcome = OutcomeNeedsRevision
	}
	return ev
}

// gradeExternalAction grades a task whose execution touched the outside world.
// performed says the action reached that world; responseLost says its answer
// never returned. Only both together produce indeterminate: the effect may
// already have happened, so the core cannot claim success or failure, cannot
// set a ready verdict and cannot justify a retry (ADR 0039 §5). The durable lease
// key travels with the outcome so the caller can correlate the unknown effect.
// Every other combination falls through to the ordinary rubric grading.
func gradeExternalAction(w domain.Watch, rubric ResultRubric, iteration int, performed, responseLost bool, leaseKey string) RubricEvaluation {
	if !performed || !responseLost {
		return gradeResult(w, rubric, iteration, false)
	}

	maxIterations := rubric.MaxIterations
	if maxIterations <= 0 {
		maxIterations = defaultOutcomeIterations
	}
	if maxIterations > maxOutcomeIterations {
		maxIterations = maxOutcomeIterations
	}
	if iteration < 0 {
		iteration = 0
	}
	return RubricEvaluation{
		Outcome:       OutcomeIndeterminate,
		Iteration:     iteration,
		MaxIterations: maxIterations,
		Criteria: []CriterionEvaluation{{
			Criterion:   CriterionExternalActionResponse,
			Passed:      false,
			Explanation: "external action was performed but its response was lost; effect unknown",
		}},
		LeaseKey: leaseKey,
	}
}

// evaluateCriterion turns Watch facts into one explained criterion result.
func evaluateCriterion(w domain.Watch, id CriterionID) CriterionEvaluation {
	switch id {
	case CriterionRunFinished:
		if w.Status == domain.StatusClosed || !w.ClosedAt.IsZero() {
			return passedCriterion(id, "run reached a terminal state")
		}
		return CriterionEvaluation{Criterion: id, Passed: false,
			Explanation: "run has not reached a terminal state; status=" + string(w.Status)}
	case CriterionRequiredChecksOK:
		failed := failedRequiredCheckNames(w)
		if len(failed) == 0 {
			return passedCriterion(id, "no required check failed")
		}
		return CriterionEvaluation{Criterion: id, Passed: false,
			Explanation: "required checks failed: " + strings.Join(failed, ", ")}
	case CriterionMergeReady:
		status := strings.ToUpper(w.Merge.MergeStateStatus)
		if strings.ToUpper(w.Merge.Mergeable) == "MERGEABLE" && status != "DIRTY" && status != "BEHIND" {
			return passedCriterion(id, "merge is clean")
		}
		return CriterionEvaluation{Criterion: id, Passed: false,
			Explanation: fmt.Sprintf("mergeable=%s mergeStateStatus=%s", w.Merge.Mergeable, w.Merge.MergeStateStatus)}
	case CriterionVerdictReady:
		if w.Verdict == domain.VerdictReady {
			return passedCriterion(id, "verdict is ready")
		}
		return CriterionEvaluation{Criterion: id, Passed: false,
			Explanation: "verdict=" + string(w.Verdict)}
	default:
		return CriterionEvaluation{Criterion: id, Passed: false,
			Explanation: "unknown criterion"}
	}
}

func passedCriterion(id CriterionID, why string) CriterionEvaluation {
	return CriterionEvaluation{Criterion: id, Passed: true, Explanation: why}
}

// failedRequiredCheckNames lists, in stable order, the required checks whose
// conclusion or state counts as a failure (WORKER-PROMPT §4.6).
func failedRequiredCheckNames(w domain.Watch) []string {
	names := make([]string, 0)
	for name, check := range w.Checks {
		if check.Required && isFailedCheck(check) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// GradeResult is the separate result check. It re-reads the Watch from the
// store and grades it against rubric, so its verdict does not depend on how the
// agent reported the run. Grading stamps no delivery fact: readAt/claimedAt
// stay owned by the notification path (ADR 0014).
func (h *GetResultHandler) GradeResult(ctx context.Context, watchID string, rubric ResultRubric, iteration int, interrupted bool) (RubricEvaluation, error) {
	_ = ctx
	if watchID == "" {
		return RubricEvaluation{}, domain.NewAppError(domain.CodeInvalidInput, "grade_result: watchID is required")
	}
	if h == nil || h.store == nil {
		return RubricEvaluation{}, domain.NewAppError(domain.CodePersistFailed, "grade_result: store is nil")
	}
	w, err := h.store.Watch(watchID)
	if err != nil {
		return RubricEvaluation{}, mapResultReadError(err, "grade_result: watch")
	}
	return gradeResult(w, rubric, iteration, interrupted), nil
}

// GradeExternalAction is the ADR 0039 §5 entry point. It grades a task after an
// external action and reports indeterminate when that action ran but its answer
// was lost. leaseKey is the durable obligation key from get_result; it is
// returned with the indeterminate outcome and nowhere else. Grading reads the
// Watch only and stamps no delivery fact: a delivery-ladder indeterminate
// attempt in ADR 0017 never turns into a task result here.
func (h *GetResultHandler) GradeExternalAction(ctx context.Context, watchID string, rubric ResultRubric, iteration int, performed, responseLost bool, leaseKey string) (RubricEvaluation, error) {
	_ = ctx
	if watchID == "" {
		return RubricEvaluation{}, domain.NewAppError(domain.CodeInvalidInput, "grade_external_action: watchID is required")
	}
	if h == nil || h.store == nil {
		return RubricEvaluation{}, domain.NewAppError(domain.CodePersistFailed, "grade_external_action: store is nil")
	}
	w, err := h.store.Watch(watchID)
	if err != nil {
		return RubricEvaluation{}, mapResultReadError(err, "grade_external_action: watch")
	}
	return gradeExternalAction(w, rubric, iteration, performed, responseLost, leaseKey), nil
}

// AttachGetResult replaces the startup stub with the real tool. Wiring it into
// NewServer is a separate integration task, so this helper is opt-in.
func AttachGetResult(server *mcpsdk.Server, handler *GetResultHandler) {
	if server == nil || handler == nil {
		return
	}
	server.RemoveTools("beacon_get_result")
	no := false
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "beacon_get_result",
		Description: "Read a Watch result without logs and mark its content as read.",
		Annotations: &mcpsdk.ToolAnnotations{
			DestructiveHint: &no,
			IdempotentHint:  true,
		},
	}, handler.Handle)
}

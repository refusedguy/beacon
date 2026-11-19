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
	"path"
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

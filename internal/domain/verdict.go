// Package domain holds the Beacon entities, status enums and port interfaces.
// This file implements the verdict engine as a pure function over the §4.6
// decision table. It reads no clock, performs no I/O and never mutates its
// arguments. Adapters supply the snapshot and execute the returned wake
// targets.
package domain

import (
	"path"
	"strings"
	"time"
)

// Snapshot is one CI observation of a pull request at a single instant. All
// fields are values supplied by the caller; Evaluate never fetches them.
//
// Rollup is the normalized statusCheckRollup state (SUCCESS, FAILURE, ERROR,
// PENDING, ...). Failures lists the names of the checks that actually failed;
// SKIPPED and NEUTRAL results never appear there. Checks carries the observed
// contexts so fixtures can model missing required checks; Evaluate itself only
// reads Rollup and Failures from them.
//
// HeadSHA is the observed headRefOid; WatchHeadSHA is the SHA the Watch was
// registered for. An empty WatchHeadSHA disables the head_changed rule.
//
// Flaky carries the per-watch flaky configuration and the per-check retry
// budget already consumed for this SHA. A retry never increments Used: the
// engine is pure, so the caller records the consumption when it runs the re-run.
//
// At is the evaluation instant. DeadlineAt, NoChecksSince and NoChecksTimeout
// drive the timeout rule; zero At disables both time-based checks so that a
// snapshot without a clock still evaluates deterministically.
//
// SuitesValid reports that the check-suite composition behind a cached result
// was validated for this SHA. A cache keyed by SHA alone is not valid: a re-run
// keeps the SHA but resets suites (WORKER-PROMPT §4.6), so SuitesValid must be
// false after a rerequest.
type Snapshot struct {
	State            string
	IsDraft          bool
	HeadSHA          string
	WatchHeadSHA     string
	Rollup           string
	Mergeable        string
	MergeStateStatus string
	ReviewDecision   string
	Checks           map[string]Check
	Failures         []string
	Flaky            FlakyConfig
	MissingRequired  bool
	At               time.Time
	DeadlineAt       time.Time
	NoChecksSince    time.Time
	NoChecksTimeout  time.Duration
	SuitesValid      bool

	// BaseFailedChecks maps check names to their observed failure on the base
	// branch. EvaluatePR uses it to separate a failure introduced by the PR from
	// one already present on main; a nil or empty map means no base failure was
	// observed.
	BaseFailedChecks map[string]bool
	// BaseHeadSHA is the base ref OID read from the same batch as the PR. It is
	// the GraphQL ref.target.oid of the base branch, carried here so a stale
	// base is machine visible. An empty value disables the stale-base rule.
	BaseHeadSHA string
	// BaseChecksKnown reports that the base check state was actually observed.
	// A nil FailedChecks map from an unknown base is not proof that main is
	// green, so the stale-base rule requires this flag as well.
	BaseChecksKnown bool
	// MergeError is the last merge attempt error reported by the provider. It is
	// carried for diagnostics; it does not enter the verdict rules.
	MergeError string

	// Ship carries the observed facts the compiled-in ship formula reads
	// (ADR 0040 §2). Callers fill it from deterministic sources: Build and
	// Audit from required CI checks, TDD and Trivial from explicit conditions
	// in code. The zero value means no facts were observed, so Evaluate never
	// grants the ready verdict on facts the caller did not supply.
	Ship ShipFacts
}

// WakeKind distinguishes an ordinary pointer from the two adapter actions the
// verdict table requires. The zero value is an ordinary pointer.
type WakeKind string

const (
	WakePointer     WakeKind = ""             // deliver a pointer to the subscriber
	WakeHeadChanged WakeKind = "head_changed" // close the old Watch, create a new one
	WakeRestPin     WakeKind = "rest_pin"     // ping lazy mergeable=UNKNOWN via REST
	// RerunChecks names the re-run action for a bounded flaky retry. The target
	// also carries the check names in WakeTarget.RerunChecks.
	RerunChecks WakeKind = "rerun_checks"
)

// WakeTarget is one action the caller must perform after Evaluate. Role is the
// subscriber role to wake; an empty Role means the target is an adapter action
// (WakeRestPin). RerunChecks is populated only for VerdictRetry and lists the
// failing flaky checks to pass to `gh run rerun --failed`. Reason carries the
// machine-readable cause of an actionable verdict, so a receiver can act
// without triaging CI by hand. An empty reason keeps older wakes unchanged.
type WakeTarget struct {
	Role        Role
	Kind        WakeKind
	RerunChecks []string
	Reason      string
}

// ReasonStaleBaseAfterMainFix names the rebase whose PR checks stayed red
// after main was fixed. The PR is behind a base whose checks are green, so a
// rebase on the current main is the next action, not a manual failure triage.
const ReasonStaleBaseAfterMainFix = "stale_base_after_main_fix"

// Evaluate computes the verdict and wake targets for s under policy p. The
// rules are applied in the exact priority order of WORKER-PROMPT §4.6; the
// first matching row wins.
func Evaluate(s Snapshot, p Policy) (Verdict, []WakeTarget) {
	// 1. MERGED/CLOSED is terminal: close the Watch and wake everyone.
	switch strings.ToUpper(s.State) {
	case "MERGED":
		return VerdictMerged, wakeAll(WakePointer)
	case "CLOSED":
		return VerdictClosed, wakeAll(WakePointer)
	}

	// 2. headRefOid moved: the old Watch is closed and a replacement is
	// created for the same subscribers (ADR 0019). VerdictWatching keeps the
	// result inside the existing Verdict enum; the head_changed wake targets
	// carry the instruction for the adapter.
	if s.HeadSHA != "" && s.WatchHeadSHA != "" && s.HeadSHA != s.WatchHeadSHA {
		return VerdictWatching, wakeAll(WakeHeadChanged)
	}

	// 3-4. Merge-state blockers route to the executor for a rebase.
	status := strings.ToUpper(s.MergeStateStatus)
	if status == "DIRTY" || (status == "BEHIND" && p.RequireUpToDate) {
		return VerdictRebase, wakeExecutor(WakePointer)
	}

	// 5-6. A failed rollup is either a bounded flaky retry or keep_working.
	// §4.6 requires every failing check to be flaky and below its budget.
	rollup := strings.ToUpper(s.Rollup)
	if rollup == "FAILURE" || rollup == "ERROR" {
		if rerun := flakyRetryChecks(s); len(rerun) > 0 {
			return VerdictRetry, []WakeTarget{
				{Role: RoleExecutor, RerunChecks: rerun},
				{Role: RoleOrchestrator},
			}
		}
		return VerdictKeepWorking, wakeExecutor(WakePointer)
	}

	// 7. Ready needs a clean success, a valid suite composition, no draft,
	// no missing required check and no unresolved review request. The
	// compiled-in ship formula (ADR 0040 §2) is the last gate: green checks
	// alone do not make a change ready to ship. With no observed facts the
	// formula fails and Evaluate falls through to the in-between verdicts.
	if rollup == "SUCCESS" && strings.ToUpper(s.Mergeable) == "MERGEABLE" &&
		canMergeStatus(status, p.RequireUpToDate) && !s.IsDraft &&
		!s.MissingRequired && s.SuitesValid &&
		(p.IgnoreReviews || strings.ToUpper(s.ReviewDecision) != "CHANGES_REQUESTED") &&
		ShipAllowed(s.Ship) {
		return VerdictReady, wakeOrchestrator(WakePointer)
	}

	// 8. A lazy UNKNOWN mergeable needs a REST pin, not a subscriber wake.
	if strings.ToUpper(s.Mergeable) == "UNKNOWN" {
		return VerdictWatching, []WakeTarget{{Kind: WakeRestPin}}
	}

	// 9. Deadline or no-checks timeout escalates through the orchestrator.
	if timedOut(s) {
		return VerdictTimeout, wakeOrchestrator(WakePointer)
	}

	// 10. Pending checks (ACTION_REQUIRED/WAITING/QUEUED) and all other
	// in-between states are plain watching with nobody to wake.
	return VerdictWatching, nil
}

// EvaluatePR computes the verdict for the real CI cycle. It deliberately differs
// from Evaluate in two places: a terminal MERGED/CLOSED wakes nobody (the caller
// closes all subscribers as finished), and a failure already present on the base
// branch is blocked immediately so the orchestrator can fix main instead of
// asking several executors to rebase the same broken base. The rules are pure
// and applied in strict priority order; the first match wins.
func EvaluatePR(s Snapshot, p Policy) (Verdict, []WakeTarget) {
	// 1-2. Terminal states close every subscriber without waking anyone.
	switch strings.ToUpper(s.State) {
	case "MERGED":
		return VerdictMerged, nil
	case "CLOSED":
		return VerdictClosed, nil
	}

	// 3. A moved head invalidates the Watch and creates a successor for the
	// executor. The orchestrator is not waked for a pure head change.
	if s.HeadSHA != "" && s.WatchHeadSHA != "" && s.HeadSHA != s.WatchHeadSHA {
		return VerdictWatching, []WakeTarget{{Role: RoleExecutor, Kind: WakeHeadChanged}}
	}

	status := strings.ToUpper(s.MergeStateStatus)
	mergeable := strings.ToUpper(s.Mergeable)
	rollup := strings.ToUpper(s.Rollup)

	// 4. Merge-state blockers route to the executor for a rebase. A fixer may
	// perform the same repair, so the caller is free to route to either role.
	if status == "DIRTY" || (status == "BEHIND" && p.RequireUpToDate) {
		return VerdictRebase, wakeExecutor(WakePointer)
	}

	// 5. A failure already present on the base branch takes priority over both
	// the retry budget and the rollup: re-running a check on main can never make
	// it green. Read the raw report before harmless-result filtering, so a base
	// failure cannot be softened into a retry or a ready verdict by a stale
	// SUCCESS conclusion on the caller's snapshot. An informational check is
	// still excluded: required:false cannot block a Watch even when it fails
	// on main. A check absent from s.Checks stays fail-closed.
	//
	// _BASELINE report: the cardindex check failed on the base branch with
	// required:false and blocked 4 watchdogs (PR 1105, 1190, 1191, 1193).
	for _, name := range s.Failures {
		if !s.BaseFailedChecks[name] {
			continue
		}
		if c, observed := s.Checks[name]; observed && !c.Required {
			continue
		}
		return VerdictBlocked, wakeOrchestrator(WakePointer)
	}
	failed := realFailures(s)
	// The rollup counts every check, including informational ones. When the
	// per-check facts are present and no required check failed, a red rollup
	// came from a failed required:false gate and must not move the verdict.
	// Rebuild the rollup from the checks that can block: a pending required
	// check keeps the Watch in watching, an all-green set can reach ready.
	if len(s.Checks) > 0 && rollup == "FAILURE" && len(failed) == 0 {
		rollup = "SUCCESS"
		for _, c := range s.Checks {
			if !c.Required {
				continue
			}
			green := NormalizeConclusion(c.Conclusion) == "SUCCESS" ||
				NormalizeConclusion(c.State) == "SUCCESS"
			if !green {
				rollup = "PENDING"
				break
			}
		}
	}
	if rollup == "FAILURE" || rollup == "ERROR" || len(failed) > 0 {
		// A PR left behind by a fixed main must rebase, not keep working. The
		// behindBy signal arrives from the batch as mergeStateStatus=BEHIND;
		// the base OID and its green rollup come from the same observation.
		// Requiring all three keeps a PR with an unknown or still broken main
		// on its existing retry or keep_working path, so this never forces a
		// rebase of every red PR.
		if status == "BEHIND" && s.BaseChecksKnown && s.BaseHeadSHA != "" &&
			len(s.BaseFailedChecks) == 0 {
			return VerdictRebase, []WakeTarget{{
				Role: RoleExecutor, Kind: WakePointer, Reason: ReasonStaleBaseAfterMainFix,
			}}
		}
		retry := s
		retry.Failures = failed
		if rerun := flakyRetryChecks(retry); len(rerun) > 0 {
			return VerdictRetry, []WakeTarget{{Role: RoleExecutor, RerunChecks: rerun}}
		}
		return VerdictKeepWorking, wakeExecutor(WakePointer)
	}

	// 6. A clean rollup or a doc-only PR with no checks is ready only after the
	// merge gates and the review gate pass. The ship formula is not part of this
	// cycle: the caller has already verified the change before asking for CI.
	docOnly := len(s.Checks) == 0 && status == "CLEAN" && mergeable == "MERGEABLE"
	if (rollup == "SUCCESS" || docOnly) && mergeable == "MERGEABLE" &&
		canMergeStatus(status, p.RequireUpToDate) && !s.IsDraft &&
		!s.MissingRequired && s.SuitesValid && reviewOK(s, p) {
		return VerdictReady, wakeOrchestrator(WakePointer)
	}

	// 7. A lazy UNKNOWN mergeable needs a REST pin, not a subscriber wake.
	if mergeable == "UNKNOWN" {
		return VerdictWatching, []WakeTarget{{Kind: WakeRestPin}}
	}

	// 8. Deadline or no-checks timeout escalates through the orchestrator.
	if timedOut(s) {
		return VerdictTimeout, wakeOrchestrator(WakePointer)
	}

	// 9. Pending and in-between states watch silently.
	return VerdictWatching, nil
}

// NormalizeConclusion maps a raw check conclusion or status to one of four
// canonical states: SUCCESS, FAILURE, CANCELLED and PENDING. Adapters that feed
// Snapshot.Checks use this mapping, so the verdict engine never re-decides
// whether PASS, SUCCESSFUL or NEUTRAL mean a passing check. CANCELLED and
// CANCELED deliberately share one value. The app layer mirrors this vocabulary
// because ADR 0005 forbids an app import of domain.
func NormalizeConclusion(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "PASS", "SUCCESS", "SUCCESSFUL", "SKIPPED", "NEUTRAL":
		return "SUCCESS"
	case "FAIL", "FAILURE", "ERROR", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED":
		return "FAILURE"
	case "CANCELLED", "CANCELED":
		return "CANCELLED"
	default:
		return "PENDING"
	}
}

// realFailures returns the failure names that represent an actual failed check.
// SKIPPED and NEUTRAL conclusions are passes for this decision table even when
// a caller included them in Failures, so they never trigger a block or retry.
// A check observed as non-required is informational and is dropped as well.
func realFailures(s Snapshot) []string {
	out := make([]string, 0, len(s.Failures))
	seen := make(map[string]bool, len(s.Failures))
	for _, name := range s.Failures {
		if seen[name] {
			continue
		}
		seen[name] = true
		if NormalizeConclusion(name) == "SUCCESS" {
			continue
		}
		c, observed := s.Checks[name]
		// An explicitly non-required check cannot block the verdict. A check
		// absent from the map is not informational, so a caller that reports a
		// failure without per-check detail stays fail-closed.
		if observed && !c.Required {
			continue
		}
		if harmlessConclusion(c) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// harmlessConclusion reports whether a listed check actually passed, including
// the SKIPPED and NEUTRAL aliases. A check absent from Snapshot.Checks has no
// conclusion to soften the caller's failure report, so it stays a failure.
func harmlessConclusion(c Check) bool {
	return NormalizeConclusion(c.Conclusion) == "SUCCESS" ||
		NormalizeConclusion(c.State) == "SUCCESS"
}

// reviewOK applies the review gate. IgnoreReviews lets a Watch proceed without
// an approval; otherwise a CHANGES_REQUESTED decision holds it back.
func reviewOK(s Snapshot, p Policy) bool {
	return p.IgnoreReviews || strings.ToUpper(s.ReviewDecision) != "CHANGES_REQUESTED"
}

// canMergeStatus implements the mergeStateStatus part of row 7. BEHIND is
// acceptable only when requireUpToDate is off; otherwise row 4 already fired.
func canMergeStatus(status string, requireUpToDate bool) bool {
	switch status {
	case "CLEAN", "HAS_HOOKS":
		return true
	case "BEHIND":
		return !requireUpToDate
	default:
		return false
	}
}

// timedOut reports that the caller-supplied instant reached a deadline or a
// no-checks timeout. With no At the time-based rules are skipped.
func timedOut(s Snapshot) bool {
	if s.At.IsZero() {
		return false
	}
	if !s.DeadlineAt.IsZero() && !s.At.Before(s.DeadlineAt) {
		return true
	}
	if !s.NoChecksSince.IsZero() && s.NoChecksTimeout > 0 &&
		!s.At.Before(s.NoChecksSince.Add(s.NoChecksTimeout)) {
		return true
	}
	return false
}

// flakyRetryChecks returns the checks to re-run when every failure is flaky
// and under its per-check budget. An empty failure set is never a retry: there
// is nothing to re-run, so the engine falls through to keep_working.
func flakyRetryChecks(s Snapshot) []string {
	if len(s.Failures) == 0 || s.Flaky.MaxRetries <= 0 {
		return nil
	}
	seen := make(map[string]bool, len(s.Failures))
	rerun := make([]string, 0, len(s.Failures))
	for _, name := range s.Failures {
		if !flakyMatch(s.Flaky.Names, name) || s.Flaky.Used[name] >= s.Flaky.MaxRetries {
			return nil
		}
		if !seen[name] {
			seen[name] = true
			rerun = append(rerun, name)
		}
	}
	return rerun
}

// flakyMatch matches a check name against the flaky patterns. Patterns are
// globs as defined by path.Match; an invalid pattern never matches.
func flakyMatch(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if ok, err := path.Match(pattern, name); err == nil && ok {
			return true
		}
	}
	return false
}

func wakeAll(kind WakeKind) []WakeTarget {
	return []WakeTarget{{Role: RoleExecutor, Kind: kind}, {Role: RoleOrchestrator, Kind: kind}}
}

func wakeExecutor(kind WakeKind) []WakeTarget {
	return []WakeTarget{{Role: RoleExecutor, Kind: kind}}
}

func wakeOrchestrator(kind WakeKind) []WakeTarget {
	return []WakeTarget{{Role: RoleOrchestrator, Kind: kind}}
}

// ConflictType is the machine-readable kind of a structured conflict. Callers
// branch on this value instead of parsing an error message (ADR 0033 §2).
type ConflictType string

const (
	// ConflictTypeMerge is a merge conflict reported by the CI provider.
	ConflictTypeMerge ConflictType = "merge"
	// ConflictTypeFileOwnership is a conflict between callers that claim the
	// same file paths.
	ConflictTypeFileOwnership ConflictType = "file_ownership"
)

// ConflictResult is the structured response for a conflict. Type and Paths
// carry the detail that a plain error message would otherwise hide. Message
// is optional human context and is never the only source of information.
type ConflictResult struct {
	Type    ConflictType `json:"type"`
	Paths   []string     `json:"paths"`
	Message string       `json:"message,omitempty"`
}

// NewConflictResult builds a ConflictResult. Paths are trimmed, empty entries
// and duplicates are dropped while keeping the caller's order. The Paths
// slice is always non-nil so an empty conflict serializes as [] rather than
// null.
func NewConflictResult(typ ConflictType, paths []string, message string) ConflictResult {
	out := ConflictResult{Type: typ, Paths: []string{}}
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out.Paths = append(out.Paths, p)
	}
	out.Message = message
	return out
}

// Empty reports whether c carries no conflict detail. A zero ConflictResult is
// not a valid conflict response.
func (c ConflictResult) Empty() bool {
	return c.Type == "" && len(c.Paths) == 0 && c.Message == ""
}

// ReviewVerdict is the outcome an independent reviewer returns at the review
// gate (ADR 0038 §4). It is a separate concept from Verdict: Verdict is what CI
// evaluation observed for one Watch revision, while ReviewVerdict is the
// reviewer's decision on whether the code may ship. The three canonical string
// values are also the journal form, so a verdict can be recorded verbatim.
type ReviewVerdict string

const (
	// ReviewShip passes the gate; the task may complete.
	ReviewShip ReviewVerdict = "SHIP"
	// ReviewFixFirst returns the task to the executor for changes.
	ReviewFixFirst ReviewVerdict = "FIX-FIRST"
	// ReviewBlocked stops the task with a reason.
	ReviewBlocked ReviewVerdict = "BLOCKED"
)

// Valid reports whether v is one of the three defined review verdicts. Any
// other value, including the zero value, fails the gate rather than passing it.
func (v ReviewVerdict) Valid() bool {
	switch v {
	case ReviewShip, ReviewFixFirst, ReviewBlocked:
		return true
	default:
		return false
	}
}

// ParseReviewVerdict converts an external verdict string into a ReviewVerdict.
// Surrounding whitespace is trimmed, but the canonical spelling is otherwise
// exact. An unknown value is rejected so it cannot silently pass the gate.
func ParseReviewVerdict(s string) (ReviewVerdict, bool) {
	v := ReviewVerdict(strings.TrimSpace(s))
	if !v.Valid() {
		return "", false
	}
	return v, true
}

// ShipFacts is the set of observed facts the compiled-in ship formula reads.
// Every field is produced by code from a deterministic source, never by a
// free-form model opinion. Build and Audit come from the required CI checks;
// TDD records that the change was produced test-first; Trivial records that
// the change matched a triviality condition declared explicitly in code.
// The struct carries no configuration, so changing a fact cannot change the
// rule that consumes it.
type ShipFacts struct {
	Build   bool
	Audit   bool
	TDD     bool
	Trivial bool
}

// ShipAllowed is the compiled-in ship formula from ADR 0040 §2:
//
//	ship ⇒ build ∧ audit ∧ (tdd ∨ trivial)
//
// It is a pure function over facts: the same facts always give the same
// answer, and the function reads no clock, config, prompt or mutable state.
// The rule itself lives in code and takes no policy to override it, so a
// model cannot reconfigure or argue around it. Changing the rule requires a
// code change and a review, never a setting. This is immutability of the rule,
// not a replacement of the ADR 0001 verdict service: that service applies the
// formula to observed facts but cannot alter it.
func ShipAllowed(f ShipFacts) bool {
	return f.Build && f.Audit && (f.TDD || f.Trivial)
}

// GateMetricFacts is the set of observed quantities of a gate check with a
// measurable magnitude (ADR 0049 §5). MedianRatio is the observed median ratio
// of the measurement; Threshold is the magnitude floor that ratio must reach.
// PValue is the observed significance of the change; Alpha is the significance
// ceiling it must fall below. The struct carries no configuration or policy, so
// changing a fact cannot change the rule that consumes it.
type GateMetricFacts struct {
	MedianRatio float64
	Threshold   float64
	PValue      float64
	Alpha       float64
}

// GateMetricFlag reports whether the flag is set for a gate check with a
// measurable magnitude (ADR 0049 §5). Both conditions must hold at once:
//
//	median ratio >= threshold  AND  p-value < alpha
//
// A check that satisfies only one of the two never sets the flag, so magnitude
// cannot replace significance and significance cannot replace magnitude. This
// is a rule for gate metrics only. It is not the compiled-in ship formula of
// T3.67 (ShipAllowed), it takes no policy to override it, and it does not feed
// the §4.6 CI decision table of Evaluate.
//
// The function is pure over its facts: it reads no clock, config or mutable
// state and gives the same answer on every call. An unobserved or malformed
// quantity fails closed, because any comparison with NaN is false and zero
// values cannot satisfy both conditions at once.
func GateMetricFlag(f GateMetricFacts) bool {
	return f.MedianRatio >= f.Threshold && f.PValue < f.Alpha
}

// Phase is one step of a task plan. Done records that the agent finished the
// step. A done phase does not by itself close the task: under ADR 0042 §4 it
// still owes proof before the completion gate allows the close.
type Phase struct {
	ID   string
	Done bool
}

// Proof is the verifiable trace an agent leaves for a completed phase. It is
// recorded next to the task and enters the archive with approval (ADR 0042 §4).
// PhaseID binds the proof to a phase, Artifact is the reference a reviewer can
// re-check, and Approved records that the archive accepted it. An empty
// artifact or a missing approval cannot close a task.
type Proof struct {
	PhaseID  string
	Artifact string
	Approved bool
}

// CompletionOutcome is the completion gate decision. A REWORK result returns
// the task to the agent. This is a closing condition, not the review gate from
// ADR 0038 §4: the gate never returns a Verdict or ReviewVerdict and never
// changes either one.
type CompletionOutcome string

const (
	// CompletionAllow means every phase is done and carries approved proof.
	CompletionAllow CompletionOutcome = "ALLOW"
	// CompletionRework means a condition failed and the task stays in work.
	CompletionRework CompletionOutcome = "REWORK"
)

// CompletionReason is the machine-readable cause of a REWORK decision. Callers
// branch on it instead of parsing a message, as with ConflictType.
type CompletionReason string

const (
	CompletionReasonNoPlan          CompletionReason = "no_plan"
	CompletionReasonOpenPhase       CompletionReason = "open_phase"
	CompletionReasonMissingProof    CompletionReason = "missing_proof"
	CompletionReasonProofNoTrace    CompletionReason = "proof_no_trace"
	CompletionReasonProofUnapproved CompletionReason = "proof_unapproved"
)

// CompletionResult is the gate answer. Reason and PhaseID are set only for
// REWORK so a caller can name the exact condition that blocked the close.
type CompletionResult struct {
	Outcome CompletionOutcome `json:"outcome"`
	Reason  CompletionReason  `json:"reason,omitempty"`
	PhaseID string            `json:"phase,omitempty"`
}

// CompletionGate decides whether the agent may close a task. Both conditions
// of ADR 0042 §4 must hold: every plan phase is done, and every phase carries
// an approved, verifiable proof. Any failed condition returns the task to work
// with a reason rather than flipping a verdict. This is distinct from the ship
// formula (T3.67) and from the outcome rubric (T3.31): it checks only phases
// and proof. The function is pure, so it reads no clock or state and mutates
// neither phases nor proofs.
func CompletionGate(phases []Phase, proofs []Proof) CompletionResult {
	if len(phases) == 0 {
		return CompletionResult{Outcome: CompletionRework, Reason: CompletionReasonNoPlan}
	}
	for _, phase := range phases {
		if !phase.Done {
			return CompletionResult{Outcome: CompletionRework, Reason: CompletionReasonOpenPhase, PhaseID: phase.ID}
		}
		proof, ok := proofForPhase(proofs, phase.ID)
		if !ok {
			return CompletionResult{Outcome: CompletionRework, Reason: CompletionReasonMissingProof, PhaseID: phase.ID}
		}
		if strings.TrimSpace(proof.Artifact) == "" {
			return CompletionResult{Outcome: CompletionRework, Reason: CompletionReasonProofNoTrace, PhaseID: phase.ID}
		}
		if !proof.Approved {
			return CompletionResult{Outcome: CompletionRework, Reason: CompletionReasonProofUnapproved, PhaseID: phase.ID}
		}
	}
	return CompletionResult{Outcome: CompletionAllow}
}

// proofForPhase returns the proof bound to phaseID. A proof for another phase
// does not count, so a task cannot borrow one phase's trace to close another.
func proofForPhase(proofs []Proof, phaseID string) (Proof, bool) {
	for _, proof := range proofs {
		if proof.PhaseID == phaseID {
			return proof, true
		}
	}
	return Proof{}, false
}

// ExternalCheck is the observed result of one check that Beacon declared and
// an external verifier ran outside the agent. ExitCode is the process exit
// code: 0 means pass, any other value means fail. The agent's own account of
// its work is never an input, so it cannot supply or change the exit code.
type ExternalCheck struct {
	Name     string
	ExitCode int
}

// ExternalVerdict is the decision of the external verification gate. It is a
// separate concept from Verdict (T3.67, one CI observation of a Watch) and
// ReviewVerdict (ADR 0038 §4, a reviewer's SHIP/FIX-FIRST/BLOCKED decision):
// this verdict is computed only from exit codes of checks Beacon declared and
// ran outside the agent (ADR 0044 §3).
type ExternalVerdict string

const (
	// ExternalPass means every declared check was observed with exit code 0.
	ExternalPass ExternalVerdict = "PASS"
	// ExternalFail means a declared check failed or was not observed. An empty
	// declaration also fails closed.
	ExternalFail ExternalVerdict = "FAIL"
)

// CheckOutcome is one declared check in the on-disk evidence. Observed is
// false when the external verifier produced no exit code for the check, so a
// missing run stays visible on disk and never passes. Passed is true only for
// an observed check with exit code 0.
type CheckOutcome struct {
	Name     string `json:"name"`
	ExitCode int    `json:"exit_code"`
	Observed bool   `json:"observed"`
	Passed   bool   `json:"passed"`
}

// ExternalEvidence is the record an adapter writes to disk after the external
// checks ran. It carries the verdict and every declared check with the exit
// code behind it so a reviewer can re-check the decision from the on-disk
// facts alone. Nothing the agent reported is part of the record: the verdict
// is an external check of the result, not the agent's proof (T3.88) and not
// the compiled-in ship formula (T3.67).
type ExternalEvidence struct {
	Verdict ExternalVerdict `json:"verdict"`
	Checks  []CheckOutcome  `json:"checks"`
}

// EvaluateExternal computes the external verdict and its evidence from the
// checks Beacon declared and the exit codes an external verifier observed. It
// is pure: it runs no process, reads no clock and writes no file. An adapter
// runs the declared checks and persists the returned evidence; this function
// only decides and records.
//
// The gate fails closed. An empty declaration never passes, because an empty
// panel is not a clean run. Every declared check must have an observed exit
// code of 0; a missing observation or any non-zero exit code fails the gate.
// Only declared names and observed exit codes enter the decision, so an
// agent's own claim about its work cannot move the verdict. Evidence keeps
// the declaration order and drops duplicate names; when a check is observed
// more than once the first exit code wins, and observations for checks that
// were not declared are ignored.
func EvaluateExternal(declared []string, observed []ExternalCheck) ExternalEvidence {
	evidence := ExternalEvidence{
		Verdict: ExternalFail,
		Checks:  []CheckOutcome{},
	}
	if len(declared) == 0 {
		return evidence
	}

	exitBy := make(map[string]int, len(observed))
	for _, c := range observed {
		if _, ok := exitBy[c.Name]; !ok {
			exitBy[c.Name] = c.ExitCode
		}
	}

	seen := make(map[string]bool, len(declared))
	allPass := true
	for _, name := range declared {
		if name == "" {
			// A malformed declaration cannot name a real check, so it
			// fails the gate instead of being silently dropped.
			allPass = false
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		exit, ok := exitBy[name]
		outcome := CheckOutcome{
			Name:     name,
			ExitCode: exit,
			Observed: ok,
			Passed:   ok && exit == 0,
		}
		evidence.Checks = append(evidence.Checks, outcome)
		if !outcome.Passed {
			allPass = false
		}
	}

	if len(evidence.Checks) == 0 || !allPass {
		return evidence
	}
	evidence.Verdict = ExternalPass
	return evidence
}

// ReviewerExitCode is the strict exit code of a parsed external reviewer
// verdict (ADR 0047, feature 3). It is deliberately a distinct type from the
// Verdict of T3.67, the ReviewVerdict of ADR 0038 §4 and the ExternalVerdict
// of T3.98. None of those parses reviewer prose into a fail-closed code: this
// one does, and only this one. In particular it is not the indeterminate of
// T3.64, which names an action that ran but whose answer was lost; here an
// unclear answer is a rejection, not an honest "unknown".
type ReviewerExitCode int

const (
	// ReviewerExitApprove is code 0: the reviewer clearly approved.
	ReviewerExitApprove ReviewerExitCode = 0
	// ReviewerExitChangesRequested is code 3: the reviewer clearly asked for
	// changes, so the change may not ship as is.
	ReviewerExitChangesRequested ReviewerExitCode = 3
	// ReviewerExitReject is code 4: the fail-closed rejection. An unclear
	// answer lands here, and so does any blocking review, because a block
	// cannot be softened to an approve by any other signal in the answer.
	ReviewerExitReject ReviewerExitCode = 4
)

// reviewerNegation marks a token that cancels the marker right after it, so
// "not approved" and "no block" do not read as an approve and a block.
// Contractions are de-apostrophized by reviewerTokens: can't becomes cant.
var reviewerNegation = map[string]bool{
	"not": true, "no": true, "never": true, "none": true,
	"cannot": true, "cant": true, "dont": true, "wont": true,
	"without": true, "withhold": true, "withholding": true,
	"neither": true, "nor": true,
}

// reviewerTokens lowercases text and splits it into ASCII word tokens. An
// apostrophe is dropped so contractions normalise (can't -> cant); every
// other non-alphanumeric rune becomes a separator. Reviewer verdict markers
// are English words, so punctuation never changes which marker is seen.
func reviewerTokens(text string) []string {
	s := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r == '\'':
			return -1
		default:
			return ' '
		}
	}, strings.ToLower(text))
	return strings.Fields(s)
}

// reviewerHas reports whether phrase occurs as a run of tokens and is not
// immediately preceded by a negation token. Matching is by whole tokens, so
// "approved" does not hide inside "unapproved" and "block" does not match a
// substring of another word.
func reviewerHas(tokens []string, phrase ...string) bool {
	if len(phrase) == 0 || len(phrase) > len(tokens) {
		return false
	}
	for i := 0; i+len(phrase) <= len(tokens); i++ {
		matched := true
		for j, word := range phrase {
			if tokens[i+j] != word {
				matched = false
				break
			}
		}
		if !matched || (i > 0 && reviewerNegation[tokens[i-1]]) {
			continue
		}
		return true
	}
	return false
}

// reviewerApproved reports a clear approve signal in the answer.
func reviewerApproved(tokens []string) bool {
	for _, word := range []string{"approve", "approved", "approves", "approval", "lgtm"} {
		if reviewerHas(tokens, word) {
			return true
		}
	}
	for _, phrase := range [][]string{{"ship", "it"}, {"looks", "good", "to", "me"}} {
		if reviewerHas(tokens, phrase...) {
			return true
		}
	}
	return false
}

// reviewerChanged reports a clear request-for-changes signal in the answer.
func reviewerChanged(tokens []string) bool {
	for _, word := range []string{"reject", "rejected", "rejects", "nack"} {
		if reviewerHas(tokens, word) {
			return true
		}
	}
	for _, phrase := range [][]string{
		{"changes", "requested"}, {"request", "changes"},
		{"requested", "changes"}, {"change", "requested"},
		{"fix", "first"}, {"needs", "changes"},
		{"changes", "required"}, {"must", "fix"},
	} {
		if reviewerHas(tokens, phrase...) {
			return true
		}
	}
	return false
}

// reviewerBlocked reports a blocking signal in the answer. The block family
// is checked before every other signal by ParseReviewerExitCode, so a block
// always wins and is never softened by an approve elsewhere in the text.
func reviewerBlocked(tokens []string) bool {
	for _, word := range []string{
		"block", "blocks", "blocked", "blocker", "blockers", "blocking",
		"veto", "vetoed", "vetoes",
	} {
		if reviewerHas(tokens, word) {
			return true
		}
	}
	return false
}

// ParseReviewerExitCode turns an external reviewer's free-form answer into one
// of the three strict codes of ADR 0047 feature 3. The function is pure: it
// reads only the text, runs no I/O and never mutates its input. The rules are
// applied in this fixed priority order:
//
//  1. Any blocking signal returns code 4. A block wins over every other
//     signal and cannot be softened to an approve.
//  2. Both an approve and a changes signal in one answer are contradictory,
//     so the answer is unclear and returns code 4.
//  3. A clear changes signal returns code 3; a clear approve returns code 0.
//  4. No recognised signal, empty text, whitespace or hedging returns code 4.
//     An unclear answer is a rejection, never a pass.
//
// Only the canonical markers above count. A reviewer who writes anything
// else has not produced an approve, so the gate fails closed.
func ParseReviewerExitCode(answer string) ReviewerExitCode {
	tokens := reviewerTokens(answer)
	switch {
	case reviewerBlocked(tokens):
		return ReviewerExitReject
	case reviewerApproved(tokens) && reviewerChanged(tokens):
		return ReviewerExitReject
	case reviewerChanged(tokens):
		return ReviewerExitChangesRequested
	case reviewerApproved(tokens):
		return ReviewerExitApprove
	default:
		return ReviewerExitReject
	}
}

// GreenBlockedVerdict is the separate "green but blocked" verdict of ADR 0059
// §3: a pull request whose CI is green and ready to ship, but which an open
// blocking task holds. A task in this verdict suspends its run and frees its
// execution slot, so the scheduler can start another task while it waits.
//
// This is a scheduling status, not another CI verdict. It never replaces the
// Verdict of T3.67, the ExternalVerdict of T3.98, a parked Watch (T3.21/T3.60)
// or a BLOCKED review verdict (ADR 0038). None of those frees a slot on its
// own, so none of them is duplicated here.
type GreenBlockedVerdict string

const (
	// GreenBlockedNone means the PR is not green-but-blocked, so its task keeps
	// its execution slot. This is the zero value.
	GreenBlockedNone GreenBlockedVerdict = ""
	// GreenBlocked means the PR is green and ready but an open blocker holds
	// the run, so the task frees its execution slot.
	GreenBlocked GreenBlockedVerdict = "green_blocked"
)

// ReleasesSlot reports whether a task carrying verdict v frees its execution
// slot. Only the green-but-blocked verdict does. A green PR without a blocker,
// a red PR, a parked Watch and a BLOCKED review all keep their slot until they
// are handled by their own machinery.
func (v GreenBlockedVerdict) ReleasesSlot() bool {
	return v == GreenBlocked
}

// EvaluateGreenBlocked computes the green-but-blocked verdict for one CI
// observation s under policy p, plus the observed state of the blocking task.
// The verdict applies only when BOTH conditions hold: EvaluatePR classifies s as
// VerdictReady (green CI or a doc-only PR, mergeable, valid suites, passing
// review) and the blocker is open. EvaluatePR is the engine the CI cycle
// publishes, so a slot is freed for exactly the Watch that CI called ready.
// The ship formula is not part of this decision: a doc-only PR is ready for the
// real cycle without Ship facts. "Green" is the engine's own ready decision
// over caller-supplied CI facts, not a flag the agent can set, so this verdict
// does not lift the CI-only verification boundary of ADR 0022. A green PR with
// no blocker keeps its slot; a red PR with a blocker is not this verdict and
// keeps its slot too. When the blocker closes, the verdict returns to
// GreenBlockedNone and the task takes a slot again.
//
// The function is pure: it runs no check, reads no clock or config, and mutates
// neither s nor p. The same facts always give the same verdict.
func EvaluateGreenBlocked(s Snapshot, p Policy, blockerOpen bool) GreenBlockedVerdict {
	verdict, _ := EvaluatePR(s, p)
	if blockerOpen && verdict == VerdictReady {
		return GreenBlocked
	}
	return GreenBlockedNone
}

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
)

// WakeTarget is one action the caller must perform after Evaluate. Role is the
// subscriber role to wake; an empty Role means the target is an adapter action
// (WakeRestPin). RerunChecks is populated only for VerdictRetry and lists the
// failing flaky checks to pass to `gh run rerun --failed`.
type WakeTarget struct {
	Role        Role
	Kind        WakeKind
	RerunChecks []string
}

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

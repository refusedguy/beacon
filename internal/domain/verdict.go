// Package domain holds the Beacon entities, status enums and port interfaces.
// This file implements the verdict engine as a pure function over the §4.6
// decision table. It reads no clock, performs no I/O and never mutates its
// arguments. Adapters supply the snapshot and execute the returned wake
// targets.
package domain

import (
	"bytes"
	"encoding/json"
	"path"
	"regexp"
	"sort"
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

// The two diagnostics that make a silent required:false decision visible.
// The verdict engine stays pure and never logs. An adapter emits these events
// at Watch registration, so an operator can explain a red PR that never moves
// without reading the exclusion rules in this file.
const (
	EventInformationalChecksOnly     = "informational_checks_only"
	EventInformationalFailureIgnored = "informational_failure_ignored"
)

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

// EvaluatePR computes the verdict for the real CI cycle. It reads only s and p,
// performs no I/O and never mutates its arguments. The first matching row wins.
//
// The rows below are the Watch blocker priority. cibridge.Refresh applies its
// own gates before calling this function and keeps the same order. There a moved
// head replaces the Watch, a blocked Watch waits, a spent rebase budget parks
// the Watch, and a stalled in_work subscriber is replaced. Then the engine
// applies these rows:
//
//  1. MERGED or CLOSED is terminal and wakes nobody.
//  2. A moved head watches and wakes only the executor with head_changed.
//  3. DIRTY, or BEHIND with requireUpToDate, rebases.
//  4. A failure already on the base branch blocks through the orchestrator.
//  5. A flaky failure under budget retries. A stable failure keeps working. A
//     PR left behind a repaired main rebases with a stale_base_after_main_fix
//     reason.
//  6. A clean rollup or a doc-only PR reaches ready after the merge and review
//     gates pass.
//  7. An UNKNOWN mergeable pins the REST view.
//  8. A deadline or a no-checks timeout escalates through the orchestrator.
//  9. Every other state watches silently.
//
// A failure on the head and a failure already on the base take different rows.
// The head failure reaches row 5 and asks its executor to keep working. The base
// failure stops at row 4 and asks the orchestrator to fix main. That split keeps
// several executors from rebasing the same broken base.
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
	anyRequired := hasRequiredCheck(s)

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
		if c, observed := s.Checks[name]; observed && !requiredGate(name, c, anyRequired) {
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
		for name, c := range s.Checks {
			if !requiredGate(name, c, anyRequired) {
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

// IsInformationalCheckName reports whether a check is informational by name.
// ATX-C01 leaves e2e and ci-provider-probe outside the repository's required
// status checks, so their failures never block a Watch even when the provider
// cannot tell which checks are required.
func IsInformationalCheckName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "e2e", "ci-provider-probe":
		return true
	default:
		return false
	}
}

// hasRequiredCheck reports whether any observed check carries an explicit
// required flag. GitHub's isRequired(pullRequestNumber:) answers false for
// every context when the base branch has no required status checks and no
// rulesets. Without one true answer the provider cannot tell a deliberately
// informational check from a check that lost its branch protection.
func hasRequiredCheck(s Snapshot) bool {
	for _, c := range s.Checks {
		if c.Required {
			return true
		}
	}
	return false
}

// requiredGate reports whether a check can block the verdict. A check marked
// required always can. When no observed check was marked required, the
// observation has no required-check list to trust, so every observed check
// except the known informational names is treated as required. This keeps a
// failed build from being silently dropped on a repository without branch
// protection while preserving the informational-only decision.
func requiredGate(name string, c Check, anyRequired bool) bool {
	if c.Required {
		return true
	}
	return !anyRequired && !IsInformationalCheckName(name)
}

// realFailures returns the failure names that represent an actual failed check.
// SKIPPED and NEUTRAL conclusions are passes for this decision table even when
// a caller included them in Failures, so they never trigger a block or retry.
// A check explicitly excluded from the required list is informational and is
// dropped as well. When no required check was observed at all, only the known
// informational names are dropped.
func realFailures(s Snapshot) []string {
	out := make([]string, 0, len(s.Failures))
	seen := make(map[string]bool, len(s.Failures))
	anyRequired := hasRequiredCheck(s)
	for _, name := range s.Failures {
		if seen[name] {
			continue
		}
		seen[name] = true
		if NormalizeConclusion(name) == "SUCCESS" {
			continue
		}
		c, observed := s.Checks[name]
		// A check absent from the map is not informational, so a caller that
		// reports a failure without per-check detail stays fail-closed.
		if observed && !requiredGate(name, c, anyRequired) {
			continue
		}
		if harmlessConclusion(c) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// InformationalCheckDiagnostics reports the informational decisions that the
// verdict engine drops without a trace. allInformational is true when at least
// one check was observed and none of them can block the Watch. ignored lists
// the failed checks that were filtered out solely because they are
// informational: they appear in Failures, are present in Checks and have a
// non-success conclusion. Both decisions use the same requiredGate as the
// verdict engine, so a failed check never looks informational when no check
// was marked required and the provider cannot tell required checks apart.
// The verdict itself is untouched, and an empty or fully required check set
// produces no diagnostics.
func InformationalCheckDiagnostics(s Snapshot) (allInformational bool, ignored []string) {
	if len(s.Checks) == 0 {
		return false, nil
	}
	anyRequired := hasRequiredCheck(s)
	for name, c := range s.Checks {
		if requiredGate(name, c, anyRequired) {
			allInformational = false
			break
		}
		allInformational = true
	}
	seen := make(map[string]bool, len(s.Failures))
	for _, name := range s.Failures {
		if seen[name] {
			continue
		}
		seen[name] = true
		c, observed := s.Checks[name]
		if !observed || requiredGate(name, c, anyRequired) || harmlessConclusion(c) {
			continue
		}
		ignored = append(ignored, name)
	}
	sort.Strings(ignored)
	return allInformational, ignored
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

// dedupInOrder returns the first occurrence of each normalized value in order.
// norm rewrites a value before the membership test; keep drops unwanted values.
func dedupInOrder[T comparable](values []T, norm func(T) T, keep func(T) bool) []T {
	out := make([]T, 0, len(values))
	seen := make(map[T]bool, len(values))
	for _, v := range values {
		v = norm(v)
		if !keep(v) || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// NewConflictResult builds a ConflictResult. Paths are trimmed, empty entries
// and duplicates are dropped while keeping the caller's order. The Paths
// slice is always non-nil so an empty conflict serializes as [] rather than
// null.
func NewConflictResult(typ ConflictType, paths []string, message string) ConflictResult {
	return ConflictResult{
		Type:    typ,
		Paths:   dedupInOrder(paths, strings.TrimSpace, func(s string) bool { return s != "" }),
		Message: message,
	}
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
//
// Log is the check output the adapter captured. When the adapter supplies a
// log, EvaluateExternal requires a suite-run marker in it, so a process that
// exited 0 without running a test cannot pass. A check without a log keeps
// the legacy exit-code decision, which lets adapters that do not capture a
// log keep working.
//
// TestExitCode is the exit code of the test command itself, captured before
// the wrapper runs its cleanup. TestExitCodeObserved is true only when the
// adapter supplied that separate number. A wrapper can exit 0 after a
// successful cleanup even when the tests failed, so the test exit code takes
// precedence over ExitCode in the decision when it was reported. When it was
// not reported, ExitCode stays the only source of the decision and older
// adapters keep their behavior.
type ExternalCheck struct {
	Name                 string
	ExitCode             int
	Log                  string
	TestExitCode         int
	TestExitCodeObserved bool
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
	// ExternalPartial means at least one declared check passed and at least
	// one failed or was not observed. It reports progress to the orchestrator.
	// It is not a clear gate: only ExternalPass clears the external check, so
	// a partial verdict never opens a merge.
	ExternalPartial ExternalVerdict = "PARTIAL"
	// ExternalFail means no declared check passed, a malformed declaration was
	// present, or the declaration was empty. An empty declaration fails closed.
	ExternalFail ExternalVerdict = "FAIL"
)

// ExternalReasonNoTestsRan is the CheckOutcome reason for a check that exited
// 0 without a suite-run marker in its log. A passing check has an empty
// reason, so the on-disk JSON of an ordinary run keeps its original shape.
const ExternalReasonNoTestsRan = "no_tests_ran"

// ExternalReasonEvidenceMismatch is the CheckOutcome reason for a check whose
// deciding exit code and log statuses describe different runs: the code is
// non-zero while the parsed log has no FAILED or ERROR line. A patch can print
// its own PASSED lines from a conftest hook, so the log alone cannot prove the
// run. The contradiction is recorded on the check instead of being hidden
// behind a plain fail (SB-10).
const ExternalReasonEvidenceMismatch = "evidence_mismatch"

// LogStatusMap counts the status tokens parsed from a check log. Keys are the
// words a runner prints (PASSED, FAILED, ERROR, SKIPPED, XFAIL, XPASS), upper
// cased. A nil map means no status line was parsed, which the evidence records
// through CheckOutcome.StatusMapObserved.
type LogStatusMap map[string]int

// CheckOutcome is one declared check in the on-disk evidence. Observed is
// false when the external verifier produced no exit code for the check, so a
// missing run stays visible on disk and never passes. Passed is true only for
// an observed check whose deciding exit code is 0. When a log was supplied,
// the log must also carry a suite-run marker or Reason records no_tests_ran.
//
// ExitCode is the wrapper's process exit code. TestExitCode is the test
// command's exit code and TestExitCodeObserved says whether the adapter
// reported one. When TestExitCodeObserved is true, Passed and the verdict use
// TestExitCode and the evidence shows both numbers, so the source of the
// deciding number is visible from the evidence alone. StatusMap is the count
// of status tokens parsed from the log and StatusMapObserved says whether the
// log carried any. A non-zero deciding code with no FAILED or ERROR in the map
// sets Reason to evidence_mismatch, so a log that contradicts the code cannot
// pass as proof of the run (SB-10). The new fields are omitted from JSON when
// nothing extra was reported, which keeps the on-disk shape of older adapters
// unchanged.
type CheckOutcome struct {
	Name                 string       `json:"name"`
	ExitCode             int          `json:"exit_code"`
	Observed             bool         `json:"observed"`
	Passed               bool         `json:"passed"`
	Reason               string       `json:"reason,omitempty"`
	TestExitCode         int          `json:"test_exit_code,omitempty"`
	TestExitCodeObserved bool         `json:"test_exit_code_observed,omitempty"`
	StatusMap            LogStatusMap `json:"status_map,omitempty"`
	StatusMapObserved    bool         `json:"status_map_observed,omitempty"`
}

// CheckProgress is the exact pass fraction of the declared checks. Passed
// counts checks whose deciding exit code was 0 and whose log, when supplied,
// carried a suite-run marker. Total counts distinct non-empty declared checks.
// The pair keeps a ratio instead of one bit, so a caller can tell 2/3 from 1/3
// and choose between a fix and a full rollback by number (SB-08).
type CheckProgress struct {
	Passed int `json:"passed"`
	Total  int `json:"total"`
}

// ExternalEvidence is the record an adapter writes to disk after the external
// checks ran. It carries the verdict, the pass fraction and every declared
// check with the codes behind it, so a reviewer can re-check the decision from
// the on-disk facts alone and see whether the code came from the test command
// or the wrapper. Progress is always filled when the declaration is non-empty,
// including a 0/N fail, so the orchestrator sees how far a run got. Nothing the
// agent reported is part of the record: the verdict is an external check of the
// result, not the agent's proof (T3.88) and not the compiled-in ship formula
// (T3.67).
type ExternalEvidence struct {
	Verdict  ExternalVerdict `json:"verdict"`
	Progress CheckProgress   `json:"progress"`
	Checks   []CheckOutcome  `json:"checks"`
}

// suiteRunMarkers are the positive signals that a test runner executed. The
// list mirrors SUITE_RAN in SWE-bench and adds the "passed" and "Ran N tests"
// forms used by pytest and unittest. Every count must be non-zero: a runner
// that starts and immediately loses its browser still prints a summary such
// as "Executed 0 of 0", and reading a zero count as evidence would turn a
// suite that never ran into a clean run.
var suiteRunMarkers = []*regexp.Regexp{
	regexp.MustCompile(`(?m)Executed [1-9][0-9]* of [0-9]+`),
	regexp.MustCompile(`(?m)TOTAL: [1-9][0-9]* (?:SUCCESS|FAILED)`),
	regexp.MustCompile(`(?m)[1-9][0-9]* passing`),
	regexp.MustCompile(`(?m)[1-9][0-9]* passed`),
	regexp.MustCompile(`(?m)Tests:\s+[1-9][0-9]*`),
	regexp.MustCompile(`(?m)Test Suites:\s+(?:[0-9]+ \w+, )*[1-9][0-9]* total`),
	regexp.MustCompile(`(?m)^# tests [1-9][0-9]*`),
	regexp.MustCompile(`(?m)^Ran [1-9][0-9]* tests?`),
	regexp.MustCompile(`(?m)[1-9][0-9]* specs?, [0-9]+ failures?`),
	regexp.MustCompile(`(?m)': ok$`),
}

// SuiteRan reports whether a check log carries evidence that a test runner
// actually executed tests. An empty log reports no run. The function is pure
// and uses only the log string, so it cannot be swayed by an agent's claim
// about its work. Adapters may call it before writing evidence, and
// EvaluateExternal calls it on the log the adapter supplied.
func SuiteRan(log string) bool {
	if log == "" {
		return false
	}
	for _, marker := range suiteRunMarkers {
		if marker.MatchString(log) {
			return true
		}
	}
	return false
}

// logStatusPattern matches the status words verbose pytest and SWE-bench logs
// print. Counting one match per occurrence keeps a summary line such as
// "3 failed" from hiding the failures it names. Case is folded because runners
// print both PASSED and passed.
var logStatusPattern = regexp.MustCompile(`(?im)\b(PASSED|FAILED|ERROR|SKIPPED|XFAIL|XPASS)\b`)

// parseLogStatusMap counts status words in a check log. It is pure and reads
// only the log string. No matching word gives a nil map, so an absent status
// map stays distinguishable from one that was parsed and found clean.
func parseLogStatusMap(log string) LogStatusMap {
	if log == "" {
		return nil
	}
	matches := logStatusPattern.FindAllStringSubmatch(log, -1)
	if len(matches) == 0 {
		return nil
	}
	counts := make(LogStatusMap, len(matches))
	for _, match := range matches {
		if len(match) > 1 {
			counts[strings.ToUpper(match[1])]++
		}
	}
	return counts
}

// evidenceConsistent reports whether a deciding exit code and a parsed status
// map describe the same run. A non-zero exit must be backed by at least one
// FAILED or ERROR status when a map was parsed. An absent map is not a
// contradiction: a log without status words says nothing about the run.
func evidenceConsistent(decidingExit int, statusMap LogStatusMap) bool {
	if decidingExit == 0 || statusMap == nil {
		return true
	}
	return statusMap["FAILED"] > 0 || statusMap["ERROR"] > 0
}

// observedExternalCheck keeps the first observation of a declared check. The
// log and the test exit code travel with the process exit code so the decision
// uses one consistent record.
type observedExternalCheck struct {
	exitCode             int
	log                  string
	testExitCode         int
	testExitCodeObserved bool
}

// EvaluateExternal computes the external verdict and its evidence from the
// checks Beacon declared and the exit codes an external verifier observed. It
// is pure: it runs no process, reads no clock and writes no file. An adapter
// runs the declared checks and persists the returned evidence; this function
// only decides and records.
//
// The gate fails closed. An empty declaration never passes, because an empty
// panel is not a clean run. Every declared check must have an observed exit
// code of 0; a missing observation or any non-zero deciding exit code fails
// the gate. The deciding code is the test command's exit code when the adapter
// reported one, and the wrapper's process exit code otherwise. A successful
// cleanup therefore cannot hide a failing test. When the adapter supplies a
// log, exit code 0 is not enough: the log must also carry a suite-run marker,
// or the check fails with reason no_tests_ran. When the log carries status
// tokens, a non-zero deciding code with no FAILED or ERROR records reason
// evidence_mismatch, so PASSED lines printed by a patch cannot stand as proof
// of a run that failed. A check with no log keeps the exit-code decision so
// existing adapters do not need to change. Only declared names, observed exit
// codes and adapter logs enter the decision, so an agent's own claim about its
// work cannot move the verdict. Evidence keeps
// the declaration order and drops duplicate names; when a check is observed
// more than once the first observation wins, and observations for checks that
// were not declared are ignored.
//
// The verdict has three values. All declared checks passing gives PASS. Some
// passing and some failing gives PARTIAL, with Progress holding the exact
// passed/total ratio. No check passing, a malformed declaration or an empty
// declaration gives FAIL. PARTIAL reports progress but never opens a merge:
// only PASS clears the gate. Progress is filled from the declared panel for
// every non-empty declaration, including FAIL with 0/N.
func EvaluateExternal(declared []string, observed []ExternalCheck) ExternalEvidence {
	evidence := ExternalEvidence{
		Verdict: ExternalFail,
		Checks:  []CheckOutcome{},
	}
	if len(declared) == 0 {
		return evidence
	}

	observedBy := make(map[string]observedExternalCheck, len(observed))
	for _, c := range observed {
		if _, ok := observedBy[c.Name]; !ok {
			observedBy[c.Name] = observedExternalCheck{
				exitCode:             c.ExitCode,
				log:                  c.Log,
				testExitCode:         c.TestExitCode,
				testExitCodeObserved: c.TestExitCodeObserved,
			}
		}
	}

	seen := make(map[string]bool, len(declared))
	passed := 0
	validDeclaration := true
	for _, name := range declared {
		if name == "" {
			// A malformed declaration cannot name a real check, so it
			// fails the gate instead of being silently dropped.
			validDeclaration = false
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		obs, ok := observedBy[name]
		// The test command's exit code decides when present. The wrapper's
		// exit code is only the fallback for adapters that do not separate
		// the test run from its cleanup.
		decidingExit := obs.exitCode
		if obs.testExitCodeObserved {
			decidingExit = obs.testExitCode
		}
		statusMap := parseLogStatusMap(obs.log)
		outcome := CheckOutcome{
			Name:                 name,
			ExitCode:             obs.exitCode,
			Observed:             ok,
			Passed:               ok && decidingExit == 0,
			TestExitCode:         obs.testExitCode,
			TestExitCodeObserved: ok && obs.testExitCodeObserved,
			StatusMap:            statusMap,
			StatusMapObserved:    statusMap != nil,
		}
		// A logged check with exit 0 but no suite-run marker did not run
		// its tests. Fail it with a reason the reviewer can see on disk.
		if outcome.Passed && obs.log != "" && !SuiteRan(obs.log) {
			outcome.Passed = false
			outcome.Reason = ExternalReasonNoTestsRan
		}
		// A non-zero code with a parsed log that reports no failures means
		// the two channels describe different runs. The check already fails
		// on the code; the reason also records that the log is not proof of
		// this run (SB-10). An absent status map is left to the exit-code
		// decision, so SB-01 and SB-02 keep their behavior.
		if !evidenceConsistent(decidingExit, statusMap) {
			outcome.Passed = false
			outcome.Reason = ExternalReasonEvidenceMismatch
		}
		evidence.Checks = append(evidence.Checks, outcome)
		if outcome.Passed {
			passed++
		}
	}

	evidence.Progress = CheckProgress{Passed: passed, Total: len(evidence.Checks)}
	total := len(evidence.Checks)
	if total == 0 || !validDeclaration {
		// No declared check, or a malformed declaration, fails closed.
		return evidence
	}
	if passed == total {
		evidence.Verdict = ExternalPass
		return evidence
	}
	if passed > 0 {
		evidence.Verdict = ExternalPartial
	}
	return evidence
}

// RegradeReasonMissing is the refusal reason for an artifact without a raw
// check log. No log means there is nothing to recheck, so the gate fails
// closed instead of reporting a PASS from a half-written artifact.
const RegradeReasonMissing = "evidence_missing"

// RegradeArtifact is the record saved in a Watch journal when the external
// checks finish. Regrade reads only this record: it never contacts the agent,
// the verifier or the CI provider and never changes the Watch. RawLog is the
// captured check output and is the proof that a run happened, so an artifact
// without it is refused. Dataset and Split name the reference set the way a
// run.json names a SWE-bench run, so a later recalculation cannot quietly
// switch the baseline. ConfigID is the configuration identity at save time.
// ExternalEvidence keeps the shape SB-01 and SB-02 defined; a regrade never
// rewrites it.
type RegradeArtifact struct {
	WatchID  string           `json:"watchId"`
	Revision uint64           `json:"revision"`
	ConfigID string           `json:"configId,omitempty"`
	Dataset  string           `json:"dataset,omitempty"`
	Split    string           `json:"split,omitempty"`
	Declared []string         `json:"declared"`
	Observed []ExternalCheck  `json:"observed"`
	RawLog   string           `json:"rawLog"`
	Evidence ExternalEvidence `json:"evidence"`
}

// RegradeReference is the baseline a caller wants to grade against. An empty
// Declared keeps the declaration saved in the artifact. Dataset and Split
// override the saved names only when set, so changing the reference set is an
// explicit act that the result records.
type RegradeReference struct {
	Dataset  string   `json:"dataset,omitempty"`
	Split    string   `json:"split,omitempty"`
	Declared []string `json:"declared,omitempty"`
}

// RegradeResult is the answer of a regrade. Evidence is recomputed from the
// saved checks; Stored is the evidence read from the artifact. Matches reports
// a byte-for-byte equality of their JSON forms. A false Matches means the
// saved evidence no longer follows from the saved facts, and the caller must
// not treat the computed verdict as the recorded one.
type RegradeResult struct {
	Evidence ExternalEvidence `json:"evidence"`
	Stored   ExternalEvidence `json:"stored"`
	Matches  bool             `json:"matches"`
	WatchID  string           `json:"watchId"`
	Revision uint64           `json:"revision"`
	ConfigID string           `json:"configId,omitempty"`
	Dataset  string           `json:"dataset,omitempty"`
	Split    string           `json:"split,omitempty"`
}

// RegradeError is a refusal to regrade. Reason is a stable machine code, so a
// caller can print evidence_missing verbatim instead of guessing from prose.
type RegradeError struct {
	Reason string
	Detail string
}

func (e *RegradeError) Error() string {
	if e.Detail == "" {
		return "regrade: " + e.Reason
	}
	return "regrade: " + e.Reason + ": " + e.Detail
}

// Regrade recomputes the verdict from an artifact saved earlier. It runs no
// check, reads no clock and writes no Watch, so the Watch revision and the set
// of observations stay as they were. A missing raw log is refused with reason
// evidence_missing. An empty declaration still fails closed through
// EvaluateExternal.
func Regrade(a RegradeArtifact) (RegradeResult, error) {
	return RegradeWithReference(a, RegradeReference{})
}

// RegradeWithReference recomputes against an explicit reference set. Passing a
// different Declared list is how a reviewer checks a disputed verdict against
// another baseline without running anything: the same observations can yield
// a different verdict, and the result names the baseline that was used.
func RegradeWithReference(a RegradeArtifact, ref RegradeReference) (RegradeResult, error) {
	if strings.TrimSpace(a.RawLog) == "" {
		return RegradeResult{}, &RegradeError{
			Reason: RegradeReasonMissing,
			Detail: "watch " + a.WatchID + " has no saved raw log",
		}
	}
	declared := a.Declared
	if ref.Declared != nil {
		declared = ref.Declared
	}
	result := RegradeResult{
		Evidence: EvaluateExternal(declared, a.Observed),
		Stored:   a.Evidence,
		WatchID:  a.WatchID,
		Revision: a.Revision,
		ConfigID: a.ConfigID,
		Dataset:  a.Dataset,
		Split:    a.Split,
	}
	if ref.Dataset != "" {
		result.Dataset = ref.Dataset
	}
	if ref.Split != "" {
		result.Split = ref.Split
	}
	want, err := json.Marshal(result.Stored)
	if err != nil {
		return RegradeResult{}, err
	}
	got, err := json.Marshal(result.Evidence)
	if err != nil {
		return RegradeResult{}, err
	}
	result.Matches = bytes.Equal(got, want)
	return result, nil
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

// ---- SB-03: two-tier advisory classification of infrastructure failures ----
//
// SWE-bench keeps a broken environment separate from a bad patch. The eight
// signatures below mirror swebench/harness/infra_failure.py:28-61. Two
// properties carry over unchanged. Classification is post-hoc over text the
// harness already wrote, and it is advisory: a classified Watch keeps its
// verdict, its wait stop reason and its place in the scoring denominator.
// Nothing here runs a command, opens a container or drops an instance from a
// run. The classifier reads only the text it is given and mutates no argument.

// InfraTier is the confidence level of an infrastructure failure. Environment
// faults cannot plausibly come from patching repository source. Ambiguous
// faults can come from either the environment or the patch and are reported
// separately so they are never mistaken for a confirmed environment fault.
type InfraTier string

const (
	InfraTierEnvironment InfraTier = "environment"
	InfraTierAmbiguous   InfraTier = "ambiguous"
)

// Valid reports whether t is one of the two contract tiers.
func (t InfraTier) Valid() bool {
	return t == InfraTierEnvironment || t == InfraTierAmbiguous
}

// InfraReason names one of the eight SWE-bench signatures. The string values
// are the wire form written to the wait report and to the journal.
type InfraReason string

const (
	InfraReasonBrowserLaunchFailed  InfraReason = "browser_launch_failed"
	InfraReasonDisplayUnavailable   InfraReason = "display_unavailable"
	InfraReasonOutOfMemory          InfraReason = "out_of_memory"
	InfraReasonContainerUnavailable InfraReason = "container_unavailable"
	InfraReasonNetworkUnreachable   InfraReason = "network_unreachable"
	InfraReasonMissingModule        InfraReason = "missing_module"
	InfraReasonNoTestsCollected     InfraReason = "no_tests_collected"
	InfraReasonTestsTimedOut        InfraReason = "tests_timed_out"
)

// Valid reports whether r is one of the eight contract reasons.
func (r InfraReason) Valid() bool {
	switch r {
	case InfraReasonBrowserLaunchFailed, InfraReasonDisplayUnavailable,
		InfraReasonOutOfMemory, InfraReasonContainerUnavailable,
		InfraReasonNetworkUnreachable, InfraReasonMissingModule,
		InfraReasonNoTestsCollected, InfraReasonTestsTimedOut:
		return true
	default:
		return false
	}
}

// InfraSignature is one ordered signature. Matching is first-hit-wins, so the
// order is part of the contract: a broad pattern must not shadow a specific
// one. Pattern is compiled with the multiline flag so an anchored pattern such
// as ^Killed$ matches a single line inside a longer log.
type InfraSignature struct {
	Reason  InfraReason
	Tier    InfraTier
	Pattern *regexp.Regexp
}

// infraSignatures follows the harness order in infra_failure.py:28-61. Five
// signatures are environment, three are ambiguous.
var infraSignatures = []InfraSignature{
	{InfraReasonBrowserLaunchFailed, InfraTierEnvironment, regexp.MustCompile(`(?m)Failed to launch|Failed to connect to the bus`)},
	{InfraReasonDisplayUnavailable, InfraTierEnvironment, regexp.MustCompile(`(?m)cannot open display|Missing X server|unable to open X display`)},
	{InfraReasonOutOfMemory, InfraTierEnvironment, regexp.MustCompile(`(?m)Cannot allocate memory|OutOfMemoryError|^Killed$`)},
	{InfraReasonContainerUnavailable, InfraTierEnvironment, regexp.MustCompile(`(?m)Error response from daemon|Cannot connect to the Docker daemon`)},
	{InfraReasonNetworkUnreachable, InfraTierEnvironment, regexp.MustCompile(`(?m)Could not resolve host|Temporary failure in name resolution`)},
	{InfraReasonMissingModule, InfraTierAmbiguous, regexp.MustCompile(`(?m)Cannot find module|MODULE_NOT_FOUND|ModuleNotFoundError`)},
	{InfraReasonNoTestsCollected, InfraTierAmbiguous, regexp.MustCompile(`(?m)no tests ran|collected 0 items`)},
	{InfraReasonTestsTimedOut, InfraTierAmbiguous, regexp.MustCompile(`(?m)Timeout error: \d+ seconds exceeded`)},
}

// InfraSignatures returns the eight signatures in harness order. The returned
// slice is a copy, so a caller cannot reorder the table that classification
// depends on.
func InfraSignatures() []InfraSignature {
	out := make([]InfraSignature, len(infraSignatures))
	copy(out, infraSignatures)
	return out
}

// ClassifyInfra returns the tier and reason of the first signature matching
// text, and true when one matched. Empty or unmatched text yields empty values
// and false. The function is pure: it reads no clock or store, runs no command
// and changes no verdict or scoring list.
func ClassifyInfra(text string) (InfraTier, InfraReason, bool) {
	if text == "" {
		return "", "", false
	}
	for _, signature := range infraSignatures {
		if signature.Pattern.MatchString(text) {
			return signature.Tier, signature.Reason, true
		}
	}
	return "", "", false
}

// InfraFailureRecord is one advisory journal line for a classified Watch. It
// records what was observed and when. It does not carry a verdict, a stop
// reason or a denominator decision: the Watch keeps all three.
type InfraFailureRecord struct {
	WatchID string
	Tier    InfraTier
	Reason  InfraReason
	At      time.Time
}

// InfraFailureJournal is the sink for advisory classifications. The port lives
// in domain so an app adapter can persist records without making infra import
// a sibling package. A nil journal means classification stays in the report.
type InfraFailureJournal interface {
	RecordInfraFailure(InfraFailureRecord)
}

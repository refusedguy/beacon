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
	// no missing required check and no unresolved review request.
	if rollup == "SUCCESS" && strings.ToUpper(s.Mergeable) == "MERGEABLE" &&
		canMergeStatus(status, p.RequireUpToDate) && !s.IsDraft &&
		!s.MissingRequired && s.SuitesValid &&
		(p.IgnoreReviews || strings.ToUpper(s.ReviewDecision) != "CHANGES_REQUESTED") {
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

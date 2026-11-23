package domain

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// prReadySnapshot passes every gate of EvaluatePR row 6. Individual cases
// mutate one field at a time so each assertion points at a single rule.
func prReadySnapshot() Snapshot {
	return Snapshot{
		State:            "OPEN",
		HeadSHA:          "pr-head",
		WatchHeadSHA:     "pr-head",
		Rollup:           "SUCCESS",
		Mergeable:        "MERGEABLE",
		MergeStateStatus: "CLEAN",
		ReviewDecision:   "APPROVED",
		Checks: map[string]Check{
			"ci-build": {State: "COMPLETED", Conclusion: "SUCCESS", Required: true},
		},
		SuitesValid: true,
	}
}

func prWake(role Role, kind WakeKind) []WakeTarget {
	return []WakeTarget{{Role: role, Kind: kind}}
}

func prMutate(fn func(*Snapshot)) Snapshot {
	s := prReadySnapshot()
	fn(&s)
	return s
}

func TestEvaluatePRDecisionTable(t *testing.T) {
	prAt := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		s    Snapshot
		p    Policy
		want Verdict
		wake []WakeTarget
	}{
		{
			name: "MERGED closes without waking",
			s:    prMutate(func(s *Snapshot) { s.State = "MERGED" }),
			want: VerdictMerged, wake: nil,
		},
		{
			name: "merged is case insensitive",
			s:    prMutate(func(s *Snapshot) { s.State = "merged" }),
			want: VerdictMerged, wake: nil,
		},
		{
			name: "CLOSED closes without waking",
			s:    prMutate(func(s *Snapshot) { s.State = "CLOSED" }),
			want: VerdictClosed, wake: nil,
		},
		{
			name: "head change wakes the executor only",
			s:    prMutate(func(s *Snapshot) { s.HeadSHA = "new-head" }),
			want: VerdictWatching, wake: prWake(RoleExecutor, WakeHeadChanged),
		},
		{
			name: "DIRTY forces a rebase",
			s:    prMutate(func(s *Snapshot) { s.MergeStateStatus = "DIRTY" }),
			want: VerdictRebase, wake: prWake(RoleExecutor, WakePointer),
		},
		{
			name: "BEHIND with requireUpToDate forces a rebase",
			s:    prMutate(func(s *Snapshot) { s.MergeStateStatus = "BEHIND" }),
			p:    Policy{RequireUpToDate: true},
			want: VerdictRebase, wake: prWake(RoleExecutor, WakePointer),
		},
		{
			name: "base blame blocks before a flaky retry is considered",
			s: prMutate(func(s *Snapshot) {
				s.Rollup = "FAILURE"
				s.Failures = []string{"ci-build", "ci-flaky"}
				s.BaseFailedChecks = map[string]bool{"ci-build": true}
				s.Flaky = FlakyConfig{
					Names:      []string{"*"},
					MaxRetries: 3,
					Used:       map[string]int{},
				}
			}),
			want: VerdictBlocked, wake: prWake(RoleOrchestrator, WakePointer),
		},
		{
			name: "base blame blocks even with a green rollup",
			s: prMutate(func(s *Snapshot) {
				s.Failures = []string{"ci-build"}
				s.BaseFailedChecks = map[string]bool{"ci-build": true}
			}),
			want: VerdictBlocked, wake: prWake(RoleOrchestrator, WakePointer),
		},
		{
			name: "SKIPPED and NEUTRAL are not failures",
			s: prMutate(func(s *Snapshot) {
				s.Failures = []string{"ci-skip", "ci-neutral"}
				s.Checks["ci-skip"] = Check{State: "COMPLETED", Conclusion: "SKIPPED"}
				s.Checks["ci-neutral"] = Check{State: "COMPLETED", Conclusion: "NEUTRAL"}
			}),
			want: VerdictReady, wake: prWake(RoleOrchestrator, WakePointer),
		},
		{
			name: "flaky failure under budget retries",
			s: prMutate(func(s *Snapshot) {
				s.Rollup = "FAILURE"
				s.Failures = []string{"ci-flaky"}
				s.Flaky = FlakyConfig{
					Names:      []string{"ci-*"},
					MaxRetries: 2,
					Used:       map[string]int{"ci-flaky": 0},
				}
			}),
			want: VerdictRetry,
			wake: []WakeTarget{{Role: RoleExecutor, RerunChecks: []string{"ci-flaky"}}},
		},
		{
			name: "exhausted retry budget keeps working",
			s: prMutate(func(s *Snapshot) {
				s.Rollup = "ERROR"
				s.Failures = []string{"ci-flaky"}
				s.Flaky = FlakyConfig{
					Names:      []string{"ci-*"},
					MaxRetries: 1,
					Used:       map[string]int{"ci-flaky": 1},
				}
			}),
			want: VerdictKeepWorking, wake: prWake(RoleExecutor, WakePointer),
		},
		{
			name: "non-flaky failure keeps working",
			s: prMutate(func(s *Snapshot) {
				s.Rollup = "FAILURE"
				s.Failures = []string{"ci-stable"}
				s.Flaky = FlakyConfig{Names: []string{"ci-flaky"}, MaxRetries: 2}
			}),
			want: VerdictKeepWorking, wake: prWake(RoleExecutor, WakePointer),
		},
		{
			name: "red checks behind a fixed main rebase with a reason",
			s: prMutate(func(s *Snapshot) {
				s.Rollup = "FAILURE"
				s.Failures = []string{"ci-stable"}
				s.MergeStateStatus = "BEHIND"
				s.BaseChecksKnown = true
				s.BaseHeadSHA = "main-after-fix"
			}),
			want: VerdictRebase,
			wake: []WakeTarget{{Role: RoleExecutor, Kind: WakePointer, Reason: ReasonStaleBaseAfterMainFix}},
		},
		{
			name: "stale base wins over a flaky retry",
			s: prMutate(func(s *Snapshot) {
				s.Rollup = "FAILURE"
				s.Failures = []string{"ci-flaky"}
				s.MergeStateStatus = "BEHIND"
				s.BaseChecksKnown = true
				s.BaseHeadSHA = "main-after-fix"
				s.Flaky = FlakyConfig{Names: []string{"*"}, MaxRetries: 3}
			}),
			want: VerdictRebase,
			wake: []WakeTarget{{Role: RoleExecutor, Kind: WakePointer, Reason: ReasonStaleBaseAfterMainFix}},
		},
		{
			name: "unknown base checks keep working instead of rebasing",
			s: prMutate(func(s *Snapshot) {
				s.Rollup = "FAILURE"
				s.Failures = []string{"ci-stable"}
				s.MergeStateStatus = "BEHIND"
				s.BaseHeadSHA = "main-after-fix"
			}),
			want: VerdictKeepWorking, wake: prWake(RoleExecutor, WakePointer),
		},
		{
			name: "missing base head keeps working instead of rebasing",
			s: prMutate(func(s *Snapshot) {
				s.Rollup = "FAILURE"
				s.Failures = []string{"ci-stable"}
				s.MergeStateStatus = "BEHIND"
				s.BaseChecksKnown = true
			}),
			want: VerdictKeepWorking, wake: prWake(RoleExecutor, WakePointer),
		},
		{
			name: "red checks on a clean base keep working, not rebase all PRs",
			s: prMutate(func(s *Snapshot) {
				s.Rollup = "FAILURE"
				s.Failures = []string{"ci-stable"}
				s.BaseChecksKnown = true
				s.BaseHeadSHA = "main-after-fix"
			}),
			want: VerdictKeepWorking, wake: prWake(RoleExecutor, WakePointer),
		},
		{
			name: "doc-only PR with no checks is ready",
			s: prMutate(func(s *Snapshot) {
				s.Rollup = ""
				s.Checks = nil
			}),
			want: VerdictReady, wake: prWake(RoleOrchestrator, WakePointer),
		},
		{
			name: "clean success is ready",
			s:    prReadySnapshot(),
			want: VerdictReady, wake: prWake(RoleOrchestrator, WakePointer),
		},
		{
			name: "MergeError is diagnostics, not a verdict gate",
			s:    prMutate(func(s *Snapshot) { s.MergeError = "merge conflict" }),
			want: VerdictReady, wake: prWake(RoleOrchestrator, WakePointer),
		},
		{
			name: "literal SKIPPED and NEUTRAL entries are not failures",
			s:    prMutate(func(s *Snapshot) { s.Failures = []string{"SKIPPED", "NEUTRAL"} }),
			want: VerdictReady, wake: prWake(RoleOrchestrator, WakePointer),
		},
		{
			name: "UNKNOWN mergeable pins REST before timeout",
			s: prMutate(func(s *Snapshot) {
				s.Mergeable = "UNKNOWN"
				s.At = prAt
				s.DeadlineAt = prAt.Add(-time.Minute)
			}),
			want: VerdictWatching, wake: prWake("", WakeRestPin),
		},
		{
			name: "deadline times out through the orchestrator",
			s: prMutate(func(s *Snapshot) {
				s.Rollup = "PENDING"
				s.At = prAt
				s.DeadlineAt = prAt.Add(-time.Minute)
			}),
			want: VerdictTimeout, wake: prWake(RoleOrchestrator, WakePointer),
		},
		{
			name: "draft PR watches silently",
			s:    prMutate(func(s *Snapshot) { s.IsDraft = true }),
			want: VerdictWatching, wake: nil,
		},
		{
			name: "changes requested watches silently",
			s:    prMutate(func(s *Snapshot) { s.ReviewDecision = "CHANGES_REQUESTED" }),
			want: VerdictWatching, wake: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, wake := EvaluatePR(tt.s, tt.p)
			if got != tt.want {
				t.Errorf("verdict = %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(wake, tt.wake) {
				t.Errorf("wake = %#v, want %#v", wake, tt.wake)
			}
		})
	}
}

// A fixer is a valid wake role: the caller may swap the executor target of a
// rebase for a fixer when the blocker belongs to the base branch.
func TestRoleFixerIsValidWakeRole(t *testing.T) {
	if !RoleFixer.Valid() {
		t.Fatal("RoleFixer must be a valid role")
	}
	if got, ok := ParseRole("fixer"); !ok || got != RoleFixer {
		t.Fatalf("ParseRole(fixer) = %q, %v", got, ok)
	}
	for _, p := range []WorkerPhase{
		WorkerPhaseWaiting, WorkerPhaseInWork, WorkerPhaseBlocked,
		WorkerPhaseOrphaned, WorkerPhaseFinished,
	} {
		if !p.Valid() {
			t.Errorf("worker phase %q is not valid", p)
		}
	}
	if WorkerPhase("").Valid() {
		t.Error("zero worker phase must be invalid")
	}
}

func TestPolicyWakeCap(t *testing.T) {
	for _, tt := range []struct {
		name string
		p    Policy
		want int
	}{
		{name: "zero uses the default", p: Policy{}, want: 6},
		{name: "negative uses the default", p: Policy{MaxWakesPerWatch: -1}, want: 6},
		{name: "positive value wins", p: Policy{MaxWakesPerWatch: 3}, want: 3},
		{name: "default policy is explicit", p: DefaultPolicy(), want: DefaultMaxWakesPerWatch},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.WakeCap(); got != tt.want {
				t.Errorf("WakeCap() = %d, want %d", got, tt.want)
			}
		})
	}
	if DefaultMaxWakesPerWatch != 6 {
		t.Fatalf("DefaultMaxWakesPerWatch = %d, want 6", DefaultMaxWakesPerWatch)
	}
}

// The legacy Evaluate keeps its ship-formula gate: a snapshot that is ready for
// EvaluatePR must still be watched when no ship facts were observed.
func TestEvaluatePRDoesNotChangeEvaluate(t *testing.T) {
	s := prReadySnapshot()
	got, wake := Evaluate(s, Policy{})
	if got != VerdictWatching || wake != nil {
		t.Fatalf("Evaluate = (%q, %#v), want (watching, nil)", got, wake)
	}
}

// prCheck builds one CHECK observation from the two facts the priority rows
// read: the conclusion and the required flag.
func prCheck(conclusion string, required bool) Check {
	return Check{State: "COMPLETED", Conclusion: conclusion, Required: required}
}

// prCheckSetSnapshot trips every blocker at once. Cases clear the earlier rows
// so the winner cannot change for an accidental reason.
func prCheckSetSnapshot(checks map[string]Check, failures []string) Snapshot {
	s := prReadySnapshot()
	s.HeadSHA = "moved-head"
	s.MergeStateStatus = "DIRTY"
	s.Mergeable = "UNKNOWN"
	s.Rollup = "FAILURE"
	s.Checks = checks
	s.Failures = append([]string(nil), failures...)
	s.BaseFailedChecks = map[string]bool{"ci-build": true}
	s.At = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	s.DeadlineAt = s.At.Add(-time.Minute)
	return s
}

func prSet(s Snapshot, fn func(*Snapshot)) Snapshot {
	fn(&s)
	return s
}

func prCloneSnapshot(s Snapshot) Snapshot {
	clone := s
	if s.Checks != nil {
		clone.Checks = make(map[string]Check, len(s.Checks))
		for name, c := range s.Checks {
			clone.Checks[name] = c
		}
	}
	if s.Failures != nil {
		clone.Failures = append([]string(nil), s.Failures...)
	}
	if s.BaseFailedChecks != nil {
		clone.BaseFailedChecks = make(map[string]bool, len(s.BaseFailedChecks))
		for name, failed := range s.BaseFailedChecks {
			clone.BaseFailedChecks[name] = failed
		}
	}
	if s.Flaky.Used != nil {
		clone.Flaky.Used = make(map[string]int, len(s.Flaky.Used))
		for name, used := range s.Flaky.Used {
			clone.Flaky.Used[name] = used
		}
	}
	return clone
}

// TestEvaluatePRCheckSetPriority pins first-line-wins for mixed CHECK sets.
// Each case keeps the winning row, the exact verdict text and the exact wake
// reason callers observe. Required and informational checks carry failure,
// pending and success conclusions while several rules apply at once.
func TestEvaluatePRCheckSetPriority(t *testing.T) {
	tests := []struct {
		name    string
		row     int
		why     string
		s       Snapshot
		want    Verdict
		wantTxt string
		wake    []WakeTarget
	}{
		{
			name: "terminal state",
			row:  1,
			why:  "row 1: a terminal state closes without waking even when every later row is tripped",
			s: prSet(prCheckSetSnapshot(map[string]Check{
				"ci-build": prCheck("FAILURE", true),
				"ci-lint":  prCheck("PENDING", true),
				"e2e":      prCheck("FAILURE", false),
			}, []string{"ci-build", "e2e"}), func(s *Snapshot) {
				s.State = "MERGED"
			}),
			want: VerdictMerged, wantTxt: "merged", wake: nil,
		},
		{
			name: "moved head",
			row:  2,
			why:  "row 2: a moved head wakes only the executor with head_changed before any check or base row",
			s: prCheckSetSnapshot(map[string]Check{
				"ci-build": prCheck("FAILURE", true),
				"ci-lint":  prCheck("PENDING", true),
				"e2e":      prCheck("FAILURE", false),
			}, []string{"ci-build", "e2e"}),
			want: VerdictWatching, wantTxt: "watching",
			wake: []WakeTarget{{Role: RoleExecutor, Kind: WakeHeadChanged}},
		},
		{
			name: "dirty merge state",
			row:  3,
			why:  "row 3: DIRTY rebases before the engine reads a failed check on the base",
			s: prSet(prCheckSetSnapshot(map[string]Check{
				"ci-build": prCheck("FAILURE", true),
				"ci-lint":  prCheck("PENDING", true),
				"e2e":      prCheck("FAILURE", false),
			}, []string{"ci-build", "e2e"}), func(s *Snapshot) {
				s.HeadSHA = s.WatchHeadSHA
			}),
			want: VerdictRebase, wantTxt: "rebase",
			wake: []WakeTarget{{Role: RoleExecutor, Kind: WakePointer}},
		},
		{
			name: "required failure on base",
			row:  4,
			why:  "row 4: a required failure already on the base blocks through the orchestrator before retry and rollup",
			s: prSet(prCheckSetSnapshot(map[string]Check{
				"ci-build": prCheck("FAILURE", true),
				"ci-lint":  prCheck("PENDING", true),
				"e2e":      prCheck("FAILURE", false),
			}, []string{"ci-build", "e2e"}), func(s *Snapshot) {
				s.HeadSHA = s.WatchHeadSHA
				s.MergeStateStatus = "CLEAN"
			}),
			want: VerdictBlocked, wantTxt: "blocked",
			wake: []WakeTarget{{Role: RoleOrchestrator, Kind: WakePointer}},
		},
		{
			name: "informational failure on base",
			row:  6,
			why:  "row 6: an informational failure on the base and head cannot block when a required check is green",
			s: prSet(prCheckSetSnapshot(map[string]Check{
				"ci-build":      prCheck("SUCCESS", true),
				"optional-lint": prCheck("FAILURE", false),
			}, []string{"optional-lint"}), func(s *Snapshot) {
				s.HeadSHA = s.WatchHeadSHA
				s.MergeStateStatus = "CLEAN"
				s.Mergeable = "MERGEABLE"
				s.BaseFailedChecks = map[string]bool{"optional-lint": true}
				s.At = time.Time{}
				s.DeadlineAt = time.Time{}
			}),
			want: VerdictReady, wantTxt: "ready",
			wake: []WakeTarget{{Role: RoleOrchestrator, Kind: WakePointer}},
		},
		{
			name: "required failure on head",
			row:  5,
			why:  "row 5: a required failure on the head keeps working and the informational failure beside it is dropped",
			s: prSet(prCheckSetSnapshot(map[string]Check{
				"ci-build": prCheck("FAILURE", true),
				"e2e":      prCheck("FAILURE", false),
			}, []string{"ci-build", "e2e"}), func(s *Snapshot) {
				s.HeadSHA = s.WatchHeadSHA
				s.MergeStateStatus = "CLEAN"
				s.BaseFailedChecks = nil
			}),
			want: VerdictKeepWorking, wantTxt: "keep_working",
			wake: []WakeTarget{{Role: RoleExecutor, Kind: WakePointer}},
		},
		{
			name: "flaky required failure under budget",
			row:  5,
			why:  "row 5: a flaky required failure under budget reruns only its own check, never the informational one",
			s: prSet(prCheckSetSnapshot(map[string]Check{
				"ci-flaky": prCheck("FAILURE", true),
				"e2e":      prCheck("FAILURE", false),
			}, []string{"ci-flaky", "e2e"}), func(s *Snapshot) {
				s.HeadSHA = s.WatchHeadSHA
				s.MergeStateStatus = "CLEAN"
				s.BaseFailedChecks = nil
				s.Flaky = FlakyConfig{
					Names:      []string{"ci-*"},
					MaxRetries: 2,
					Used:       map[string]int{"ci-flaky": 0},
				}
			}),
			want: VerdictRetry, wantTxt: "retry",
			wake: []WakeTarget{{Role: RoleExecutor, Kind: RerunChecks, RerunChecks: []string{"ci-flaky"}}},
		},
		{
			name: "stale base with a required failure",
			row:  5,
			why:  "row 5: a PR behind a repaired main rebases with the exact stale_base_after_main_fix reason",
			s: prSet(prCheckSetSnapshot(map[string]Check{
				"ci-build": prCheck("FAILURE", true),
			}, []string{"ci-build"}), func(s *Snapshot) {
				s.HeadSHA = s.WatchHeadSHA
				s.MergeStateStatus = "BEHIND"
				s.BaseFailedChecks = nil
				s.BaseChecksKnown = true
				s.BaseHeadSHA = "main-after-fix"
			}),
			want: VerdictRebase, wantTxt: "rebase",
			wake: []WakeTarget{{Role: RoleExecutor, Kind: WakePointer, Reason: "stale_base_after_main_fix"}},
		},
		{
			name: "pending informational check with green required check",
			row:  6,
			why:  "row 6: a pending informational check does not hold a PR whose required check succeeded",
			s: prSet(prCheckSetSnapshot(map[string]Check{
				"ci-build": prCheck("SUCCESS", true),
				"e2e":      prCheck("PENDING", false),
			}, nil), func(s *Snapshot) {
				s.HeadSHA = s.WatchHeadSHA
				s.MergeStateStatus = "CLEAN"
				s.Mergeable = "MERGEABLE"
				s.BaseFailedChecks = nil
				s.Rollup = "SUCCESS"
				s.At = time.Time{}
				s.DeadlineAt = time.Time{}
			}),
			want: VerdictReady, wantTxt: "ready",
			wake: []WakeTarget{{Role: RoleOrchestrator, Kind: WakePointer}},
		},
		{
			name: "unknown mergeable beats deadline",
			row:  7,
			why:  "row 7: mergeable=UNKNOWN pins the REST view before the deadline row is read",
			s: prSet(prCheckSetSnapshot(map[string]Check{
				"ci-lint": prCheck("PENDING", true),
			}, nil), func(s *Snapshot) {
				s.HeadSHA = s.WatchHeadSHA
				s.MergeStateStatus = "CLEAN"
				s.BaseFailedChecks = nil
				s.Rollup = "PENDING"
			}),
			want: VerdictWatching, wantTxt: "watching",
			wake: []WakeTarget{{Kind: WakeRestPin}},
		},
		{
			name: "deadline on a pending required check",
			row:  8,
			why:  "row 8: a pending required check past its deadline escalates through the orchestrator",
			s: prSet(prCheckSetSnapshot(map[string]Check{
				"ci-lint": prCheck("PENDING", true),
			}, nil), func(s *Snapshot) {
				s.HeadSHA = s.WatchHeadSHA
				s.MergeStateStatus = "CLEAN"
				s.Mergeable = "MERGEABLE"
				s.BaseFailedChecks = nil
				s.Rollup = "PENDING"
			}),
			want: VerdictTimeout, wantTxt: "timeout",
			wake: []WakeTarget{{Role: RoleOrchestrator, Kind: WakePointer}},
		},
		{
			name: "pending required check without a deadline",
			row:  9,
			why:  "row 9: a pending required check with no deadline watches silently",
			s: prSet(prCheckSetSnapshot(map[string]Check{
				"ci-lint": prCheck("PENDING", true),
			}, nil), func(s *Snapshot) {
				s.HeadSHA = s.WatchHeadSHA
				s.MergeStateStatus = "CLEAN"
				s.Mergeable = "MERGEABLE"
				s.BaseFailedChecks = nil
				s.Rollup = "PENDING"
				s.At = time.Time{}
				s.DeadlineAt = time.Time{}
			}),
			want: VerdictWatching, wantTxt: "watching", wake: nil,
		},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("row%d/%s", tt.row, tt.name), func(t *testing.T) {
			before := prCloneSnapshot(tt.s)
			p := Policy{}

			got, wake := EvaluatePR(tt.s, p)
			if got != tt.want || string(got) != tt.wantTxt {
				t.Errorf("verdict = %q (%q), want %q (%q); %s", got, string(got), tt.want, tt.wantTxt, tt.why)
			}
			if !reflect.DeepEqual(wake, tt.wake) {
				t.Errorf("wake = %#v, want %#v; %s", wake, tt.wake, tt.why)
			}
			if !reflect.DeepEqual(tt.s, before) {
				t.Errorf("EvaluatePR mutated the snapshot: %+v, want %+v", tt.s, before)
			}
			if p != (Policy{}) {
				t.Errorf("EvaluatePR mutated the policy: %+v", p)
			}
		})
	}
}

package domain

import (
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

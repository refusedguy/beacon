package core

import (
	"testing"
	"time"
)

func TestRunTransitions(t *testing.T) {
	cases := []struct {
		name  string
		state RunState
		event EventKind
		want  RunState
	}{
		{"start", RunPending, EvStart, RunRunning},
		{"idle nudge", RunRunning, EvIdleNudge, RunRunning},
		{"register pr", RunRunning, EvPRRegistered, RunWaitingCI},
		{"checks pending after spawn", RunRunning, EvChecksPending, RunWaitingCI},
		{"behind", RunWaitingCI, EvBehind, RunNeedsRebase},
		{"rebase", RunNeedsRebase, EvRebased, RunWaitingCI},
		{"failed ci", RunWaitingCI, EvChecksFailed, RunNeedsAgentFix},
		{"wake", RunNeedsAgentFix, EvAgentFixed, RunWaitingCI},
		{"green checks", RunNeedsAgentFix, EvChecksPassed, RunWaitingCI},
		{"merge", RunWaitingCI, EvMerged, RunMerged},
		{"block", RunWaitingCI, EvBlock, RunBlocked},
		{"unblock", RunBlocked, EvUnblock, RunWaitingCI},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Run{ID: "r1", State: tc.state, MaxAttempts: 3, UpdatedAt: time.Now()}
			if err := r.Apply(Event{Kind: tc.event}); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if r.State != tc.want {
				t.Fatalf("state = %s, want %s", r.State, tc.want)
			}
		})
	}
}

func TestIdleNudgeSpendsBoundedFixAttempt(t *testing.T) {
	r := &Run{ID: "r1", State: RunRunning, MaxAttempts: 2}
	for i := 1; i <= 2; i++ {
		if err := r.Apply(Event{Kind: EvIdleNudge}); err != nil {
			t.Fatalf("Apply idle nudge %d: %v", i, err)
		}
		if r.FixAttempts != i || r.State != RunRunning {
			t.Fatalf("after nudge %d: attempts=%d state=%s", i, r.FixAttempts, r.State)
		}
	}
	if err := r.Apply(Event{Kind: EvIdleNudge}); err == nil {
		t.Fatal("expected exhausted error after the last idle nudge")
	}
}

func TestRunRejectsInvalidAndExhausted(t *testing.T) {
	r := &Run{ID: "r1", State: RunWaitingCI, MaxAttempts: 1, RebaseAttempts: 1}
	if err := r.Apply(Event{Kind: EvBehind}); err != nil {
		t.Fatalf("behind: %v", err)
	}
	if err := r.Apply(Event{Kind: EvRebased}); err == nil {
		t.Fatal("expected exhausted error")
	}
	if err := r.Apply(Event{Kind: EvCancel}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := r.Apply(Event{Kind: EvUnblock}); err == nil {
		t.Fatal("expected terminal transition error")
	}
}

func TestRunStateCanTakeQueriesStayEventSpecific(t *testing.T) {
	states := []RunState{
		RunPending, RunRunning, RunWaitingCI, RunNeedsRebase,
		RunNeedsAgentFix, RunBlocked, RunMerged, RunClosed,
	}
	queries := []struct {
		name     string
		event    EventKind
		can      func(RunState) bool
		eligible map[RunState]bool
	}{
		{"behind", EvBehind, RunState.CanTakeBehind, map[RunState]bool{RunRunning: true, RunWaitingCI: true}},
		{"check failed", EvChecksFailed, RunState.CanTakeCheckFailed, map[RunState]bool{RunRunning: true, RunWaitingCI: true}},
		{"check pending", EvChecksPending, RunState.CanTakeCheckPending, map[RunState]bool{RunRunning: true, RunWaitingCI: true}},
	}
	for _, q := range queries {
		t.Run(q.name, func(t *testing.T) {
			for _, state := range states {
				want := q.eligible[state]
				if got := q.can(state); got != want {
					t.Errorf("%s query = %v, want %v", state, got, want)
				}
				r := &Run{ID: "r1", State: state, MaxAttempts: 3}
				err := r.Apply(Event{Kind: q.event})
				if want {
					if err != nil {
						t.Errorf("Apply(%s) from %s: %v", q.event, state, err)
					}
					continue
				}
				if CodeOf(err) != CodeInvalidTransition {
					t.Errorf("Apply(%s) from %s error = %v, want invalid transition", q.event, state, err)
				}
			}
		})
	}
}

func TestRebaseBudgetExhaustedHonorsCap(t *testing.T) {
	r := &Run{ID: "r1", MaxAttempts: 3, RebaseAttempts: 2}
	if !r.RebaseBudgetExhausted(2) {
		t.Fatal("two attempts should exhaust a cap of two")
	}
	if r.RebaseBudgetExhausted(3) {
		t.Fatal("two attempts should not exhaust a cap of three")
	}
	if !r.CanRebase() {
		t.Fatal("the run budget should still use MaxAttempts")
	}
	r.RebaseAttempts = 3
	if !r.RebaseBudgetExhausted(3) || r.CanRebase() {
		t.Fatal("three attempts should exhaust both the explicit cap and the run budget")
	}
}

func TestResetAttemptsClearsBothCounters(t *testing.T) {
	r := &Run{ID: "r1", RebaseAttempts: 2, FixAttempts: 1}
	r.ResetAttempts()
	if r.RebaseAttempts != 0 || r.FixAttempts != 0 {
		t.Fatalf("attempts = %d/%d, want 0/0", r.RebaseAttempts, r.FixAttempts)
	}
}

func TestRetryClosedRunWithPRReturnsToWaitingCI(t *testing.T) {
	r := &Run{ID: "r1", State: RunClosed, PRNumber: 42, Reason: "worker session exited",
		SessionID: "sess", WorkspacePath: "/ws/T-1", FixAttempts: 2, RebaseAttempts: 1}
	if err := r.Retry("retried by operator"); err != nil {
		t.Fatal(err)
	}
	if r.State != RunWaitingCI || r.PRNumber != 42 {
		t.Fatalf("state=%s pr=%d, want waiting_ci/42", r.State, r.PRNumber)
	}
	if r.FixAttempts != 0 || r.RebaseAttempts != 0 || r.Reason != "retried by operator" {
		t.Fatalf("run = %+v, want reset attempts and retry reason", r)
	}
}

func TestRetryClosedRunWithoutPRReturnsToPending(t *testing.T) {
	r := &Run{ID: "r2", State: RunClosed, SessionID: "sess", WorkspacePath: "/ws/T-2"}
	if err := r.Retry("retried by operator"); err != nil {
		t.Fatal(err)
	}
	if r.State != RunPending || r.SessionID != "" || r.WorkspacePath != "" {
		t.Fatalf("run = %+v, want pending with cleared session and worktree", r)
	}
}

func TestRetryRejectsNonClosedRun(t *testing.T) {
	r := &Run{ID: "r3", State: RunWaitingCI}
	if err := r.Retry("retried by operator"); CodeOf(err) != CodeInvalidTransition {
		t.Fatalf("error = %v, want invalid transition", err)
	}
}

func TestExternalFinalizeMergesRun(t *testing.T) {
	r := &Run{ID: "r1", State: RunWaitingCI, PRNumber: 42}
	if err := r.ExternalFinalize(Observation{PRNumber: 42, PRState: "merged", HeadSHA: "sha9"}); err != nil {
		t.Fatal(err)
	}
	if r.State != RunMerged || r.PRNumber != 42 || r.HeadSHA != "sha9" {
		t.Fatalf("run = %+v", r)
	}
	if r.Reason != "merged PR #42 via squash" {
		t.Fatalf("reason = %q", r.Reason)
	}
	if r.UpdatedAt.IsZero() {
		t.Fatal("updated at not set")
	}
}

func TestExternalFinalizeClosesRun(t *testing.T) {
	r := &Run{ID: "r1", State: RunNeedsRebase, PRNumber: 42}
	if err := r.ExternalFinalize(Observation{PRNumber: 42, PRState: "closed"}); err != nil {
		t.Fatal(err)
	}
	if r.State != RunClosed || r.Reason != "pull request closed" {
		t.Fatalf("run = %+v", r)
	}
}

func TestExternalFinalizeRejectsTerminalRun(t *testing.T) {
	r := &Run{ID: "r1", State: RunMerged}
	if err := r.ExternalFinalize(Observation{PRState: "merged"}); CodeOf(err) != CodeInvalidTransition {
		t.Fatalf("error = %v, want invalid transition", err)
	}
}

func TestReopenWithPR(t *testing.T) {
	r := &Run{ID: "r1", State: RunClosed, Reason: "workspace: conflict: refusing to remove dirty worktree /ws/T-1"}
	if err := r.ReopenWithPR(42); err != nil {
		t.Fatal(err)
	}
	if r.State != RunWaitingCI || r.PRNumber != 42 || r.Reason != "" {
		t.Fatalf("run = %+v", r)
	}
	if r.UpdatedAt.IsZero() {
		t.Fatal("updated at not set")
	}
}

func TestReopenWithPRRejectsActiveRun(t *testing.T) {
	r := &Run{ID: "r1", State: RunWaitingCI}
	if err := r.ReopenWithPR(42); CodeOf(err) != CodeInvalidTransition {
		t.Fatalf("error = %v, want invalid transition", err)
	}
	if r.State != RunWaitingCI || r.PRNumber != 0 {
		t.Fatalf("run = %+v", r)
	}
}

func TestRetryBlockedRun(t *testing.T) {
	r := &Run{ID: "r1", State: RunBlocked, RebaseAttempts: 2, FixAttempts: 3}
	if err := r.RetryBlocked("retried by operator"); err != nil {
		t.Fatal(err)
	}
	if r.State != RunWaitingCI || r.RebaseAttempts != 0 || r.FixAttempts != 0 || r.Reason != "retried by operator" {
		t.Fatalf("run = %+v", r)
	}
}

func TestRetryBlockedRejectsNonBlockedRun(t *testing.T) {
	r := &Run{ID: "r1", State: RunWaitingCI, RebaseAttempts: 2}
	if err := r.RetryBlocked("retried by operator"); CodeOf(err) != CodeInvalidTransition {
		t.Fatalf("error = %v, want invalid transition", err)
	}
	if r.RebaseAttempts != 2 {
		t.Fatalf("attempts reset on rejected retry: %+v", r)
	}
}

func TestMarkExternal(t *testing.T) {
	r := &Run{ID: "r1", State: RunWaitingCI}
	if err := r.MarkExternal(42); err != nil {
		t.Fatal(err)
	}
	if r.PRNumber != 42 || r.State != RunWaitingCI {
		t.Fatalf("run = %+v", r)
	}
}

func TestMarkExternalRejectsTerminalRun(t *testing.T) {
	r := &Run{ID: "r1", State: RunClosed}
	if err := r.MarkExternal(42); CodeOf(err) != CodeInvalidTransition {
		t.Fatalf("error = %v, want invalid transition", err)
	}
	if r.PRNumber != 0 {
		t.Fatalf("run = %+v", r)
	}
}

func TestObserveSHAIsIdempotent(t *testing.T) {
	r := &Run{ID: "r1"}
	if !r.ObserveSHA("abc") {
		t.Fatal("first observation should be new")
	}
	if r.ObserveSHA("abc") {
		t.Fatal("second observation should be deduplicated")
	}
	if !r.ObserveSHA("def") {
		t.Fatal("new commit should be observed")
	}
}

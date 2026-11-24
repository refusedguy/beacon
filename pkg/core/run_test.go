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

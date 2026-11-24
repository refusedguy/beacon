package app

import (
	"context"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestRetrySignalsReconciler(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r1", "T-1", time.Now())
	run.State = core.RunBlocked
	run.PRNumber = 42
	run.RebaseAttempts = 2
	run.FixAttempts = 2
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}

	var signals []int
	c := &Control{Store: store, OnChange: func(pr int) { signals = append(signals, pr) }}
	if err := c.Retry(ctx, "r1"); err != nil {
		t.Fatal(err)
	}

	got := store.runs["r1"]
	if got.State != core.RunWaitingCI {
		t.Fatalf("state = %s, want %s", got.State, core.RunWaitingCI)
	}
	if got.RebaseAttempts != 0 || got.FixAttempts != 0 {
		t.Fatalf("attempts = %d/%d, want 0/0", got.RebaseAttempts, got.FixAttempts)
	}
	if len(signals) != 1 || signals[0] != 42 {
		t.Fatalf("signals = %v, want [42]", signals)
	}
}

func TestRetryClosedRunWithPRReturnsToWaitingCI(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r-closed-pr", "T-1", time.Now().UTC())
	run.State = core.RunClosed
	run.PRNumber = 42
	run.SessionID = "sess-old"
	run.WorkspacePath = "/ws/T-1"
	run.Reason = "worker session exited without PR"
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}

	var signals []int
	c := &Control{Store: store, OnChange: func(pr int) { signals = append(signals, pr) }}
	if err := c.Retry(ctx, "r-closed-pr"); err != nil {
		t.Fatal(err)
	}
	got := store.runs["r-closed-pr"]
	if got.State != core.RunWaitingCI || got.PRNumber != 42 {
		t.Fatalf("state=%s pr=%d, want waiting_ci/42", got.State, got.PRNumber)
	}
	if got.Reason != "retried by operator" {
		t.Fatalf("reason = %q", got.Reason)
	}
	if len(signals) != 1 || signals[0] != 42 {
		t.Fatalf("signals = %v, want [42]", signals)
	}
}

func TestRetryClosedRunWithoutPRReturnsToPending(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r-closed-no-pr", "T-1", time.Now().UTC())
	run.State = core.RunClosed
	run.SessionID = "sess-old"
	run.WorkspacePath = "/ws/T-1"
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}

	var signals []int
	c := &Control{Store: store, OnChange: func(pr int) { signals = append(signals, pr) }}
	if err := c.Retry(ctx, "r-closed-no-pr"); err != nil {
		t.Fatal(err)
	}
	got := store.runs["r-closed-no-pr"]
	if got.State != core.RunPending || got.SessionID != "" || got.WorkspacePath != "" {
		t.Fatalf("run = %+v, want pending with cleared session and worktree", got)
	}
	if got.Reason != "retried by operator" {
		t.Fatalf("reason = %q", got.Reason)
	}
	if len(signals) != 0 {
		t.Fatalf("signals = %v, want none", signals)
	}
}

func TestRetryClosedRunRejectsActiveSibling(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	closed := core.NewRun("r-closed", "T-1", time.Now().UTC())
	closed.State = core.RunClosed
	active := core.NewRun("r-active", "T-1", time.Now().UTC())
	active.State = core.RunRunning
	for _, run := range []*core.Run{closed, active} {
		if err := store.Save(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	c := &Control{Store: store}
	err := c.Retry(ctx, "r-closed")
	if core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("error = %v, want conflict", err)
	}
	if got := store.runs["r-closed"]; got.State != core.RunClosed {
		t.Fatalf("state = %s, want closed", got.State)
	}
}

func TestNudgeCallsWakeWithOperatorGuidance(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r-nudge", "T-1", time.Now().UTC())
	run.State = core.RunWaitingCI
	run.PRNumber = 42
	run.HeadSHA = "sha42"
	run.SessionID = "sess-nudge"
	run.WorkspacePath = "/ws/T-1"
	run.Branch = "beacon/T-1"
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	h := &fakeHarness{}
	c := &Control{Store: store, Harness: h}
	if err := c.Nudge(ctx, "r-nudge", "please rebase first"); err != nil {
		t.Fatal(err)
	}
	if len(h.wakes) != 1 {
		t.Fatalf("wakes = %+v, want one", h.wakes)
	}
	w := h.wakes[0]
	if w.RunID != "r-nudge" || w.PRNumber != 42 || w.HeadSHA != "sha42" ||
		w.Reason != "operator guidance" || w.Excerpt != "please rebase first" ||
		w.Workspace != "/ws/T-1" || w.Branch != "beacon/T-1" {
		t.Fatalf("wake = %+v", w)
	}
}

func TestNudgeRejectsRunsWithoutLiveSession(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	terminal := core.NewRun("r-terminal", "T-1", time.Now().UTC())
	terminal.State = core.RunClosed
	terminal.SessionID = "sess-old"
	noSession := core.NewRun("r-no-session", "T-2", time.Now().UTC())
	noSession.State = core.RunRunning
	for _, run := range []*core.Run{terminal, noSession} {
		if err := store.Save(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	h := &fakeHarness{}
	c := &Control{Store: store, Harness: h}
	if err := c.Nudge(ctx, "r-terminal", "hello"); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("terminal error = %v, want conflict", err)
	}
	if err := c.Nudge(ctx, "r-no-session", "hello"); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("no session error = %v, want conflict", err)
	}
	if len(h.wakes) != 0 {
		t.Fatalf("wakes = %+v, want none", h.wakes)
	}
}

func TestWatchPRBindsActiveRun(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r-active", "T-1", time.Now().UTC())
	run.State = core.RunRunning
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}

	var signals []int
	c := &Control{Store: store, OnChange: func(pr int) { signals = append(signals, pr) }}
	if err := c.WatchPR(ctx, "r-active", 42); err != nil {
		t.Fatal(err)
	}
	got := store.runs["r-active"]
	if got.State != core.RunWaitingCI || got.PRNumber != 42 {
		t.Fatalf("state=%s pr=%d, want %s/42", got.State, got.PRNumber, core.RunWaitingCI)
	}
	if len(signals) != 1 || signals[0] != 42 {
		t.Fatalf("signals = %v, want [42]", signals)
	}
}

func TestWatchPRReopensRunClosedByWorkspaceCleanup(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r-closed", "T-1", time.Now().UTC())
	run.State = core.RunClosed
	run.Reason = "workspace: conflict: refusing to remove dirty worktree /ws/T-1"
	run.SessionID = "sess-r-closed"
	run.WorkspacePath = "/ws/T-1"
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}

	var signals []int
	c := &Control{Store: store, OnChange: func(pr int) { signals = append(signals, pr) }}
	if err := c.WatchPR(ctx, "r-closed", 42); err != nil {
		t.Fatal(err)
	}
	got := store.runs["r-closed"]
	if got.State != core.RunWaitingCI || got.PRNumber != 42 {
		t.Fatalf("state=%s pr=%d, want %s/42", got.State, got.PRNumber, core.RunWaitingCI)
	}
	if got.Reason != "" {
		t.Fatalf("reason = %q, want empty", got.Reason)
	}
	if len(signals) != 1 || signals[0] != 42 {
		t.Fatalf("signals = %v, want [42]", signals)
	}

	source := &fakeSource{}
	scm := &fakeSCM{obs: core.Observation{PRNumber: 42, PRState: "merged", HeadSHA: "sha2"}}
	r := &Reconciler{
		Store: store, SCM: scm, Source: source,
		Workspace: &fakeWorkspace{}, Harness: &fakeHarness{},
	}
	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if merged := store.runs["r-closed"]; merged.State != core.RunMerged {
		t.Fatalf("state = %s, want %s", merged.State, core.RunMerged)
	}
	if len(source.complete) != 1 || source.complete[0] != "T-1" {
		t.Fatalf("completed tasks = %v, want [T-1]", source.complete)
	}
	if got := store.taskStatus["T-1"]; got != "done" {
		t.Fatalf("task status = %q, want done", got)
	}
}

func TestWatchPRRejectsRunClosedByOperator(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r-cancelled", "T-1", time.Now().UTC())
	run.State = core.RunClosed
	run.Reason = "cancelled by operator"
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}

	var signals []int
	c := &Control{Store: store, OnChange: func(pr int) { signals = append(signals, pr) }}
	err := c.WatchPR(ctx, "r-cancelled", 42)
	if core.CodeOf(err) != core.CodeInvalidTransition {
		t.Fatalf("error = %v, want invalid transition", err)
	}
	if got := store.runs["r-cancelled"]; got.State != core.RunClosed || got.PRNumber != 0 {
		t.Fatalf("state=%s pr=%d, want closed/0", got.State, got.PRNumber)
	}
	if len(signals) != 0 {
		t.Fatalf("signals = %v, want none", signals)
	}
}

func TestRetryDoesNotSignalWhenRunIsNotBlocked(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := core.NewRun("r1", "T-1", time.Now())
	run.State = core.RunWaitingCI
	run.PRNumber = 42
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}

	var signals []int
	c := &Control{Store: store, OnChange: func(pr int) { signals = append(signals, pr) }}
	err := c.Retry(ctx, "r1")
	if core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("error = %v, want conflict", err)
	}
	if len(signals) != 0 {
		t.Fatalf("signals = %v, want none", signals)
	}
	if got := store.runs["r1"]; got.State != core.RunWaitingCI {
		t.Fatalf("state = %s, want %s", got.State, core.RunWaitingCI)
	}
}

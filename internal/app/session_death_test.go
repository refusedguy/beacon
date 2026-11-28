package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestSessionDeathEventClosesRunAndFreesSlot(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := makeRun("r-dead", 0, core.RunRunning)
	run.Branch = "beacon/T-1"
	run.ParentSessionID = "parent-session"
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	// A pending idle nudge must not survive the state change.
	if _, err := store.ClaimDelivery(ctx, idleNudgeKey(run)); err != nil {
		t.Fatal(err)
	}
	h := &fakeHarness{}
	ws := &fakeWorkspace{dirty: true}
	r := &Reconciler{
		Store: store, SCM: &fakeSCM{}, Harness: h, Workspace: ws,
		Changes: make(chan int, 4), BaseBranch: "main",
	}

	r.HandleHarnessEvent(ctx, core.HarnessEvent{
		RunID: "r-dead", SessionID: run.SessionID, Kind: core.HarnessEventSessionDead,
	})

	got := store.runs["r-dead"]
	if got.State != core.RunClosed || !strings.Contains(got.Reason, sessionDeadReason) {
		t.Fatalf("state=%s reason=%q, want closed with session death reason", got.State, got.Reason)
	}
	if active, err := store.FindActiveByTask(ctx, "T-1"); err != nil || active != nil {
		t.Fatalf("active run after death = %+v err=%v, want none", active, err)
	}
	if len(h.wakes) != 0 || store.deliveries[idleNudgeKey(run)] {
		t.Fatalf("wakes=%d deliveries=%v, want no nudge and released claim", len(h.wakes), store.deliveries)
	}
	if h.stops != 1 || ws.forcedRemoves != 1 {
		t.Fatalf("stops=%d forced=%d, want 1/1", h.stops, ws.forcedRemoves)
	}
	if len(h.notifications) != 1 || h.notifications[0].sessionID != "parent-session" {
		t.Fatalf("parent notifications = %+v, want one to the parent", h.notifications)
	}
}

func TestOrphanSweepStillClosesStaleRunningRun(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := makeRun("r-stale", 0, core.RunRunning)
	run.Branch = "beacon/stale"
	run.UpdatedAt = time.Now().UTC().Add(-10 * time.Minute)
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	h := &orphanHarness{fakeHarness: &fakeHarness{}, alive: false}
	r, _ := orphanReconciler(store, &fakeSCM{}, h, &fakeWorkspace{})

	r.reconcileOrphanRunning(ctx, []*core.Run{run})

	got := store.runs["r-stale"]
	if got.State != core.RunClosed || got.Reason != orphanSweepReason {
		t.Fatalf("state=%s reason=%q, want closed with sweep reason", got.State, got.Reason)
	}
	if active, err := store.FindActiveByTask(ctx, "T-1"); err != nil || active != nil {
		t.Fatalf("active run after sweep = %+v err=%v, want none", active, err)
	}
	if len(h.checked) != 1 || h.checked[0] != run.SessionID {
		t.Fatalf("checked = %v, want one liveness probe for %q", h.checked, run.SessionID)
	}
}

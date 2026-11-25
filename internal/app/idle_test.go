package app

import (
	"context"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

// changedWorkspace reports pending changes so an idle event takes the nudge
// path instead of blocking the run.
type changedWorkspace struct {
	*fakeWorkspace
	changed bool
}

func (w *changedWorkspace) HasChanges(context.Context, core.Workspace, string) (bool, error) {
	return w.changed, nil
}

func TestSecondIdleEventNudgesAgain(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := makeRun("r1", 0, core.RunRunning)
	run.Branch = "beacon/T-1"
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	h := &fakeHarness{}
	r := &Reconciler{
		Store: store, SCM: &fakeSCM{}, Harness: h,
		Workspace:  &changedWorkspace{fakeWorkspace: &fakeWorkspace{}, changed: true},
		BaseBranch: "main",
	}

	r.HandleHarnessEvent(ctx, core.HarnessEvent{RunID: "r1", Kind: "idle"})
	r.HandleHarnessEvent(ctx, core.HarnessEvent{RunID: "r1", Kind: "idle"})

	if len(h.wakes) != 2 {
		t.Fatalf("wakes = %d, want one per idle event", len(h.wakes))
	}
	if got := store.runs["r1"].State; got != core.RunRunning {
		t.Fatalf("run state = %s, want running after both nudges", got)
	}
	if len(store.deliveries) != 0 {
		t.Fatalf("deliveries = %v, want the idle nudge claim released", store.deliveries)
	}
}

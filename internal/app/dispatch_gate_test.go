package app

import (
	"context"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

func closedGateDispatcher(closed bool, tasks []core.Task, store *fakeStore, harness *fakeHarness) *Dispatcher {
	return &Dispatcher{
		Source:       &fakeSource{tasks: tasks},
		Planner:      NewPlanner(nil),
		Workspace:    &fakeWorkspace{},
		Harness:      harness,
		Store:        store,
		Concurrency:  core.Concurrency{MaxInFlight: 2, MaxActiveAgents: 1},
		DispatchOpen: &closed,
	}
}

func TestDispatchOpenFalseStopsDispatch(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	harness := &fakeHarness{}
	tasks := []core.Task{{
		ID: "T-CLOSED", Status: core.TaskAvailable,
		Scope: []core.ScopePath{core.ScopeFile("closed.go")},
	}}
	d := closedGateDispatcher(false, tasks, store, harness)

	res, err := d.Dispatch(ctx, 2)
	if err != nil {
		t.Fatalf("d.Dispatch(ctx, 2): %v", err)
	}
	if len(res.Runs) != 0 || len(res.Failures) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("closed gate returned work: %+v", res)
	}
	if len(harness.sessions) != 0 {
		t.Fatalf("closed gate spawned %d agents", len(harness.sessions))
	}
}

func TestDispatchOpenFalseStopsPromotion(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	scope := []core.ScopePath{core.ScopeFile("pending.go")}
	pending := core.NewRun("r-pending", "T-PENDING", time.Now().UTC())
	pending.Scope = scope
	if err := store.Save(ctx, pending); err != nil {
		t.Fatalf("store.Save(pending): %v", err)
	}
	harness := &fakeHarness{}
	d := closedGateDispatcher(false, []core.Task{{
		ID: "T-PENDING", Status: core.TaskAvailable, Scope: scope,
	}}, store, harness)

	started, err := d.PromotePending(ctx)
	if err != nil {
		t.Fatalf("d.PromotePending(ctx): %v", err)
	}
	if len(started) != 0 {
		t.Fatalf("closed gate promoted %d runs", len(started))
	}
	if len(harness.sessions) != 0 {
		t.Fatalf("closed gate spawned %d agents", len(harness.sessions))
	}
}

func TestWaiterTakesSlotAfterHolderCloses(t *testing.T) {
	ctx := context.Background()
	shared := core.ScopeFile("ci.yml")
	store := newFakeStore()
	holder := core.NewRun("r-holder", "T-HOLDER", time.Now().UTC())
	holder.Scope = []core.ScopePath{shared}
	if err := holder.Apply(core.Event{Kind: core.EvStart}); err != nil {
		t.Fatalf("holder.Apply(EvStart): %v", err)
	}
	if err := holder.Apply(core.Event{Kind: core.EvMerged}); err != nil {
		t.Fatalf("holder.Apply(EvMerged): %v", err)
	}
	if err := store.Save(ctx, holder); err != nil {
		t.Fatalf("store.Save(holder): %v", err)
	}

	waiter := core.Task{ID: "T-WAITER", Status: core.TaskAvailable, Scope: []core.ScopePath{shared}}
	var queried []core.ScopePath
	d := &Dispatcher{
		Source:      &fakeSource{tasks: []core.Task{waiter}},
		Planner:     NewPlanner(nil),
		Workspace:   &fakeWorkspace{},
		Harness:     &fakeHarness{},
		Store:       store,
		Concurrency: core.Concurrency{MaxInFlight: 2, MaxActiveAgents: 1},
		WaiterSource: func(_ context.Context, paths []core.ScopePath) (core.TaskID, error) {
			queried = append(queried, paths...)
			for _, path := range paths {
				if path == shared {
					return waiter.ID, nil
				}
			}
			return "", nil
		},
	}

	res, err := d.Dispatch(ctx, 1)
	if err != nil {
		t.Fatalf("d.Dispatch(ctx, 1): %v", err)
	}
	if len(res.Runs) != 1 || res.Runs[0].TaskID != waiter.ID {
		t.Fatalf("runs = %+v, want one run for %s", res.Runs, waiter.ID)
	}
	found := false
	for _, path := range queried {
		if path == shared {
			found = true
		}
	}
	if !found {
		t.Fatalf("waiter query paths = %v, want %v", queried, shared)
	}
}

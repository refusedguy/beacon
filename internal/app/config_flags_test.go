package app

import (
	"context"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestAutoRebaseDisabledSkipsUpdateBranch(t *testing.T) {
	_, store, scm, _, _ := newReconciler(t, core.VerdictNeedsRebase,
		core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open", Behind: true})
	r := &Reconciler{
		Store: store, SCM: scm, Policy: fakePolicy{decision: core.MergeDecision{Verdict: core.VerdictNeedsRebase}},
		Harness: &fakeHarness{}, Source: &fakeSource{}, Workspace: &fakeWorkspace{}, AutoRebase: false,
	}
	decision, err := r.Reconcile(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != core.VerdictWait || scm.rebases != 0 {
		t.Fatalf("decision=%+v rebases=%d", decision, scm.rebases)
	}
}

func TestDispatcherAppliesMaxAttempts(t *testing.T) {
	store := newFakeStore()
	src := &fakeSource{tasks: []core.Task{{ID: "T-1", Status: core.TaskAvailable}}}
	d := &Dispatcher{
		Source: src, Planner: NewPlanner(nil), Workspace: &fakeWorkspace{},
		Harness: &fakeHarness{}, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
		MaxAttempts: 7,
	}
	res, err := d.Dispatch(context.Background(), 1)
	if err != nil || len(res.Runs) != 1 {
		t.Fatalf("runs=%d err=%v", len(res.Runs), err)
	}
	if res.Runs[0].MaxAttempts != 7 || store.taskStatus["T-1"] != "claimed" {
		t.Fatalf("run=%+v status=%v", res.Runs[0], store.taskStatus)
	}
}

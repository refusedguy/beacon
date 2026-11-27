package app

import (
	"context"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestGreenButBehindNoopStampsObservation(t *testing.T) {
	ctx := context.Background()
	obs := core.Observation{
		PRNumber: 42, HeadSHA: "sha2", BaseSHA: "base", PRState: "open", Behind: true,
		Checks: []core.CheckResult{{Name: "ci", State: core.CheckSuccess}},
	}
	r, store, _, _, _ := newReconciler(t, core.VerdictWait, obs)
	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatalf("r.Reconcile: %v", err)
	}
	got := store.runs["r1"]
	if got.ObservedSHA != obs.HeadSHA || got.ObservedBaseSHA != obs.BaseSHA {
		t.Fatalf("observed = %q/%q, want %q/%q", got.ObservedSHA, got.ObservedBaseSHA, obs.HeadSHA, obs.BaseSHA)
	}
}

func TestApplyAndSaveAcceptsEmptyEvent(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := makeRun("r1", 42, core.RunWaitingCI)
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Store: store}
	obs := core.Observation{HeadSHA: "sha2", BaseSHA: "base"}
	if err := r.applyAndSave(ctx, run, core.Event{}, obs); err != nil {
		t.Fatalf("applyAndSave with empty event: %v", err)
	}
	if got := store.runs["r1"]; got.ObservedSHA != "sha2" || got.State != core.RunWaitingCI {
		t.Fatalf("run = %+v, want observation stamped without state change", got)
	}
}

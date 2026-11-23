package app

import (
	"context"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func newReconciler(t *testing.T, decision core.Verdict, obs core.Observation) (*Reconciler, *fakeStore, *fakeSCM, *fakeHarness, *fakeSource) {
	t.Helper()
	store := newFakeStore()
	run := makeRun("r1", 42, core.RunWaitingCI)
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	scm := &fakeSCM{obs: obs}
	h := &fakeHarness{}
	src := &fakeSource{}
	return &Reconciler{
		Store: store, SCM: scm, Policy: fakePolicy{decision: core.MergeDecision{Verdict: decision, Reason: "test"}},
		Harness: h, Source: src, Workspace: &fakeWorkspace{}, AutoRebase: true,
	}, store, scm, h, src
}

func TestReconcileWaitingCIToAutoRebase(t *testing.T) {
	r, store, scm, _, _ := newReconciler(t, core.VerdictNeedsRebase,
		core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open", Behind: true})
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	got := store.runs["r1"]
	if got.State != core.RunWaitingCI || got.RebaseAttempts != 1 || scm.rebases != 1 {
		t.Fatalf("state=%s rebaseAttempts=%d rebases=%d", got.State, got.RebaseAttempts, scm.rebases)
	}
}

func TestReconcileWaitingCIToWakeAgent(t *testing.T) {
	r, store, _, h, _ := newReconciler(t, core.VerdictNeedsAgentFix,
		core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open", FailureExcerpt: "panic: boom"})
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	got := store.runs["r1"]
	if got.State != core.RunWaitingCI || got.FixAttempts != 1 || len(h.wakes) != 1 {
		t.Fatalf("state=%s fixAttempts=%d wakes=%d", got.State, got.FixAttempts, len(h.wakes))
	}
	if h.wakes[0].Excerpt != "panic: boom" || h.wakes[0].PRNumber != 42 {
		t.Fatalf("bad wake context: %+v", h.wakes[0])
	}
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if len(h.wakes) != 1 {
		t.Fatalf("duplicate wake for same SHA: %d", len(h.wakes))
	}
}

func TestReconcileWaitingCIToMerge(t *testing.T) {
	r, store, scm, h, src := newReconciler(t, core.VerdictMerge,
		core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open"})
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	got := store.runs["r1"]
	if got.State != core.RunMerged || scm.merges != 1 || len(src.complete) != 1 || h.stops != 1 {
		t.Fatalf("state=%s merges=%d complete=%v stops=%d", got.State, scm.merges, src.complete, h.stops)
	}
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if scm.merges != 1 {
		t.Fatal("merge was not idempotent")
	}
}

package app

import (
	"context"
	"errors"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func externalReconciler(t *testing.T, state string, fixer bool) (*Reconciler, *fakeStore, *fakeSource, *fakeHarness) {
	t.Helper()
	store := newFakeStore()
	run := makeRun("r1", 42, core.RunWaitingCI)
	run.IsFixer = fixer
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{}
	if fixer {
		src.completeErr = errors.New("FIX task does not exist")
	}
	h := &fakeHarness{}
	scm := &fakeSCM{obs: core.Observation{PRNumber: 42, HeadSHA: "sha9", PRState: state}}
	r := &Reconciler{
		Store: store, SCM: scm, Policy: fakePolicy{decision: core.MergeDecision{Verdict: core.VerdictMerge}},
		Harness: h, Source: src, Workspace: &fakeWorkspace{}, AutoRebase: true,
	}
	return r, store, src, h
}

func TestManualMergeClosesRun(t *testing.T) {
	r, store, src, h := externalReconciler(t, "merged", false)
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	got := store.runs["r1"]
	if got.State != core.RunMerged || got.HeadSHA != "sha9" {
		t.Fatalf("run = %+v", got)
	}
	if len(src.complete) != 1 || store.taskStatus["T-1"] != "done" {
		t.Fatalf("complete=%v status=%v", src.complete, store.taskStatus)
	}
	if h.stops != 1 {
		t.Fatalf("stops = %d", h.stops)
	}
}

func TestManualCloseClosesRun(t *testing.T) {
	r, store, src, h := externalReconciler(t, "closed", false)
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	got := store.runs["r1"]
	if got.State != core.RunClosed {
		t.Fatalf("run = %+v", got)
	}
	if len(src.complete) != 0 || h.stops != 1 {
		t.Fatalf("complete=%v stops=%d", src.complete, h.stops)
	}
}

func TestFixerMergeSkipsTaskSourceComplete(t *testing.T) {
	r, store, src, h := externalReconciler(t, "merged", true)
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if store.runs["r1"].State != core.RunMerged {
		t.Fatalf("run = %+v", store.runs["r1"])
	}
	if len(src.complete) != 0 || h.stops != 1 {
		t.Fatalf("complete=%v stops=%d", src.complete, h.stops)
	}
}

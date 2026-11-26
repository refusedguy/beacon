package app

import (
	"context"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

// observationReconciler wires a reconciler that uses the real Decide policy so
// tests exercise the verdict chain instead of a canned decision.
func observationReconciler(t *testing.T, obs core.Observation) (*Reconciler, *fakeStore, *fakeSCM, *fakeHarness) {
	t.Helper()
	store := newFakeStore()
	run := makeRun("r1", 42, core.RunWaitingCI)
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatalf(`store.Save(context.Background(), run): %v`, err)
	}
	scm := &fakeSCM{obs: obs}
	h := &fakeHarness{}
	r := &Reconciler{
		Store: store, SCM: scm, Policy: behindAwarePolicy{}, Harness: h,
		Source: &fakeSource{}, Workspace: &fakeWorkspace{}, AutoRebase: true,
	}
	return r, store, scm, h
}

func TestBehindFailingCheckWakesAgentOnFirstObservation(t *testing.T) {
	ctx := context.Background()
	obs := core.Observation{
		PRNumber: 42, PRState: "open", HeadSHA: "sha42", BaseSHA: "base1", Behind: true,
		Checks:         []core.CheckResult{{Name: "lint", State: core.CheckFailure}},
		FailureExcerpt: "gofmt reported a diff",
	}
	r, store, scm, h := observationReconciler(t, obs)

	decision, err := r.Reconcile(ctx, 42)
	if err != nil {
		t.Fatalf(`r.Reconcile(ctx, 42): %v`, err)
	}
	if decision.Verdict != core.VerdictNeedsAgentFix {
		t.Fatalf("verdict = %s, want %s", decision.Verdict, core.VerdictNeedsAgentFix)
	}
	if !strings.Contains(decision.Reason, "lint") {
		t.Fatalf("decision reason = %q, want the failed check name", decision.Reason)
	}
	if scm.rebases != 0 {
		t.Fatalf("branch updates = %d, want 0", scm.rebases)
	}
	got := store.runs["r1"]
	if got.FixAttempts != 1 || len(h.wakes) != 1 {
		t.Fatalf("fixAttempts=%d wakes=%d, want 1/1", got.FixAttempts, len(h.wakes))
	}
	if !strings.Contains(h.wakes[0].Reason, "lint") {
		t.Fatalf("wake reason = %q, want the failed check name", h.wakes[0].Reason)
	}
	if !strings.Contains(h.wakes[0].Excerpt, "gofmt reported a diff") {
		t.Fatalf("wake excerpt = %q, want the failure excerpt", h.wakes[0].Excerpt)
	}
}

func TestBehindWithPendingChecksNeverRebases(t *testing.T) {
	ctx := context.Background()
	obs := core.Observation{
		PRNumber: 42, PRState: "open", HeadSHA: "sha42", BaseSHA: "base1", Behind: true,
		Checks: []core.CheckResult{{Name: "lint", State: core.CheckPending}},
	}
	r, store, scm, _ := observationReconciler(t, obs)

	for i := 0; i < 3; i++ {
		decision, err := r.Reconcile(ctx, 42)
		if err != nil {
			t.Fatalf("tick %d: r.Reconcile(ctx, 42): %v", i, err)
		}
		if decision.Verdict != core.VerdictNeedsRebase {
			t.Fatalf("tick %d: verdict = %s, want %s", i, decision.Verdict, core.VerdictNeedsRebase)
		}
	}
	if scm.rebases != 0 {
		t.Fatalf("branch updates = %d, want 0 across three ticks", scm.rebases)
	}
	got := store.runs["r1"]
	if got.RebaseAttempts != 0 || got.State != core.RunWaitingCI {
		t.Fatalf("state=%s rebaseAttempts=%d, want waiting_ci/0", got.State, got.RebaseAttempts)
	}
}

func TestRebaseBudgetStopsRepeatedUpdates(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	run := makeRun("r1", 42, core.RunWaitingCI)
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf(`store.Save(ctx, run): %v`, err)
	}
	scm := &fakeSCM{
		observeSeq: []core.Observation{
			{PRNumber: 42, PRState: "open", HeadSHA: "sha1", BaseSHA: "base", Behind: true},
			{PRNumber: 42, PRState: "open", HeadSHA: "sha2", BaseSHA: "base", Behind: true},
			{PRNumber: 42, PRState: "open", HeadSHA: "sha3", BaseSHA: "base", Behind: true},
			{PRNumber: 42, PRState: "open", HeadSHA: "sha4", BaseSHA: "base", Behind: true},
		},
	}
	r := &Reconciler{
		Store: store, SCM: scm,
		Policy:  fakePolicy{decision: core.MergeDecision{Verdict: core.VerdictNeedsRebase, Reason: "branch is behind base"}},
		Harness: &fakeHarness{}, Source: &fakeSource{}, Workspace: &fakeWorkspace{}, AutoRebase: true,
	}

	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(ctx, 42); err != nil {
			t.Fatalf("tick %d: r.Reconcile(ctx, 42): %v", i, err)
		}
	}
	if scm.rebases != maxAutoRebases {
		t.Fatalf("branch updates = %d, want %d", scm.rebases, maxAutoRebases)
	}
	got := store.runs["r1"]
	if got.State != core.RunBlocked || got.Reason != "rebase budget exhausted" {
		t.Fatalf("state=%s reason=%q, want blocked with rebase budget exhausted", got.State, got.Reason)
	}
}

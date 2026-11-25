package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func externalReconciler(t *testing.T, state string, fixer bool) (*Reconciler, *fakeStore, *fakeSource, *fakeHarness) {
	t.Helper()
	store := newFakeStore()
	run := makeRun("r1", 42, core.RunWaitingCI)
	run.IsFixer = fixer
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatalf(`store.Save(context.Background(), run): %v`, err)
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
		t.Fatalf(`r.Reconcile(context.Background(), 42): %v`, err)
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
		t.Fatalf(`r.Reconcile(context.Background(), 42): %v`, err)
	}
	got := store.runs["r1"]
	if got.State != core.RunClosed {
		t.Fatalf("run = %+v", got)
	}
	if len(src.complete) != 0 || h.stops != 1 {
		t.Fatalf("complete=%v stops=%d", src.complete, h.stops)
	}
}

func TestExternalMergeOrCloseNotifiesParentWithState(t *testing.T) {
	for _, tc := range []struct{ prState, wantState string }{
		{"merged", "merged"},
		{"closed", "closed"},
	} {
		t.Run(tc.prState, func(t *testing.T) {
			r, store, _, h := externalReconciler(t, tc.prState, false)
			run := store.runs["r1"]
			run.ParentSessionID = "ses_parent"
			if err := store.Save(context.Background(), run); err != nil {
				t.Fatalf(`store.Save(context.Background(), run): %v`, err)
			}
			if _, err := r.Reconcile(context.Background(), 42); err != nil {
				t.Fatalf(`r.Reconcile(context.Background(), 42): %v`, err)
			}
			if len(h.notifications) != 1 {
				t.Fatalf("notifications = %d, want 1", len(h.notifications))
			}
			got := h.notifications[0]
			if got.sessionID != "ses_parent" {
				t.Errorf("sessionID = %q, want ses_parent", got.sessionID)
			}
			for _, want := range []string{"42", "r1", tc.wantState} {
				if !strings.Contains(got.text, want) {
					t.Errorf("notification text %q missing %q", got.text, want)
				}
			}
		})
	}
}

func TestFixerMergeSkipsTaskSourceComplete(t *testing.T) {
	r, store, src, h := externalReconciler(t, "merged", true)
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatalf(`r.Reconcile(context.Background(), 42): %v`, err)
	}
	if store.runs["r1"].State != core.RunMerged {
		t.Fatalf("run = %+v", store.runs["r1"])
	}
	if len(src.complete) != 0 || h.stops != 1 {
		t.Fatalf("complete=%v stops=%d", src.complete, h.stops)
	}
}

func TestExternalMergeForceRemovesDirtyWorktree(t *testing.T) {
	r, _, _, _ := externalReconciler(t, "merged", false)
	ws := &fakeWorkspace{dirty: true}
	r.Workspace = ws
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatalf(`r.Reconcile(context.Background(), 42): %v`, err)
	}
	if ws.forcedRemoves != 1 || ws.removed != 0 {
		t.Fatalf("forcedRemoves = %d, removed = %d; want 1 and 0", ws.forcedRemoves, ws.removed)
	}
}

func TestExternalMergeRemovesCleanWorktree(t *testing.T) {
	r, _, _, _ := externalReconciler(t, "merged", false)
	ws := &fakeWorkspace{}
	r.Workspace = ws
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatalf(`r.Reconcile(context.Background(), 42): %v`, err)
	}
	if ws.removed != 1 || ws.forcedRemoves != 0 {
		t.Fatalf("removed = %d, forcedRemoves = %d; want 1 and 0", ws.removed, ws.forcedRemoves)
	}
}

// ReviewRequired mirrors the policy review gate for the epic tests that drive
// the reconciler through this fake.
func (f fakePolicy) ReviewRequired(mode core.ReviewMode, obs core.Observation) bool {
	if mode == core.ReviewModeNone {
		return false
	}
	return obs.Approvals == 0
}

type recordingGatePolicy struct {
	fakePolicy
	required bool
	calls    int
	mode     core.ReviewMode
	obs      core.Observation
}

func (g *recordingGatePolicy) ReviewRequired(mode core.ReviewMode, obs core.Observation) bool {
	g.calls++
	g.mode = mode
	g.obs = obs
	return g.required
}

func TestGuardEpicReviewUsesPolicyGate(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	epicRun(t, store, "epic-1", 55)
	gate := &recordingGatePolicy{required: true}
	r := &Reconciler{Store: store, Policy: gate, Source: epicSource(core.ReviewModeHuman)}
	run := store.runs["epic-1"]
	obs := core.Observation{PRNumber: 55, HeadSHA: "sha1", PRState: "open", Approvals: 1}

	ok, err := r.guardEpicReview(ctx, run, obs)
	if err != nil {
		t.Fatalf(`r.guardEpicReview(ctx, run, obs): %v`, err)
	}
	if ok {
		t.Fatal("policy required review but guard allowed the merge")
	}
	if gate.calls != 1 {
		t.Fatalf("ReviewRequired calls = %d, want 1", gate.calls)
	}
	if gate.mode != core.ReviewModeHuman || gate.obs.HeadSHA != obs.HeadSHA {
		t.Fatalf("gate got mode=%s obs=%+v, want human mode and observed SHA", gate.mode, gate.obs)
	}

	gate.required = false
	ok, err = r.guardEpicReview(ctx, run, obs)
	if err != nil {
		t.Fatalf(`r.guardEpicReview(ctx, run, obs): %v`, err)
	}
	if !ok {
		t.Fatal("policy cleared review but guard still blocked the merge")
	}
	if gate.calls != 2 {
		t.Fatalf("ReviewRequired calls = %d, want 2", gate.calls)
	}
}

func TestGuardEpicReviewWithoutGateHoldsMerge(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	epicRun(t, store, "epic-1", 55)
	r := &Reconciler{Store: store, Policy: behindAwarePolicy{}, Source: epicSource(core.ReviewModeNone)}

	ok, err := r.guardEpicReview(ctx, store.runs["epic-1"], core.Observation{PRNumber: 55, HeadSHA: "sha1"})
	if err != nil {
		t.Fatalf(`r.guardEpicReview(ctx, store.runs["epic-1"], core.Observation{PRNumber: 55, HeadSHA: "sha1"}): %v`, err)
	}
	if ok {
		t.Fatal("policy without a review gate must not clear the epic gate")
	}
}

func TestCleanupKeepsActiveRunWorktree(t *testing.T) {
	ws := &fakeWorkspace{dirty: true}
	r := &Reconciler{Workspace: ws, Harness: &fakeHarness{}}
	run := makeRun("r1", 42, core.RunRunning)
	if err := r.cleanupRun(context.Background(), run); err != nil {
		t.Fatalf(`r.cleanupRun(context.Background(), run): %v`, err)
	}
	if ws.forcedRemoves != 0 || ws.removed != 0 {
		t.Fatalf("forcedRemoves = %d, removed = %d; want 0 and 0", ws.forcedRemoves, ws.removed)
	}
}

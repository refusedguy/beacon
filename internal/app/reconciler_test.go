package app

import (
	"context"
	"strings"
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

func TestReconcileMergeMarksAndCommitsCardBeforeMerge(t *testing.T) {
	r, store, scm, _, src := newReconciler(t, core.VerdictMerge,
		core.Observation{PRNumber: 42, HeadSHA: "old-sha", PRState: "open"})
	scm.observeSeq = []core.Observation{
		{PRNumber: 42, HeadSHA: "old-sha", PRState: "open"},
		{PRNumber: 42, HeadSHA: "fresh-sha", PRState: "open"},
	}
	log := &callLog{}
	scm.log = log
	src.log = log
	ws := &fakeWorkspace{log: log}
	r.Workspace = ws

	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	mergedAt := -1
	for i, entry := range log.items {
		if entry == "merge" {
			mergedAt = i
			break
		}
	}
	if mergedAt < 3 || log.items[mergedAt-1] != "observe" ||
		log.items[mergedAt-2] != "commit:docs/tasks" || log.items[mergedAt-3] != "mark:done" {
		t.Fatalf("card not marked and committed before merge: %v", log.items)
	}
	if len(src.marked) != 1 || src.marked[0].id != "T-1" ||
		src.marked[0].path != "/ws/T-1" || src.marked[0].status != "done" {
		t.Fatalf("bad MarkInWorkspace call: %+v", src.marked)
	}
	if len(ws.commits) != 1 || ws.commits[0].pathspec != "docs/tasks" ||
		ws.commits[0].message != "beacon: complete task T-1" {
		t.Fatalf("bad CommitPath call: %+v", ws.commits)
	}
	if scm.mergeSHA != "fresh-sha" {
		t.Fatalf("merge SHA = %q, want fresh-sha", scm.mergeSHA)
	}
	if !store.deliveries["merge:42:fresh-sha"] {
		t.Fatalf("idempotency key not based on fresh SHA: %v", store.deliveries)
	}
}

func TestReconcileMergeSkipsCardForFixer(t *testing.T) {
	r, store, scm, _, src := newReconciler(t, core.VerdictMerge,
		core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open"})
	run := store.runs["r1"]
	run.IsFixer = true
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	log := &callLog{}
	scm.log = log
	src.log = log
	ws := &fakeWorkspace{log: log}
	r.Workspace = ws

	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if len(src.marked) != 0 || len(ws.commits) != 0 {
		t.Fatalf("fixer marked or committed card: marked=%+v commits=%+v", src.marked, ws.commits)
	}
	observes := 0
	for _, entry := range log.items {
		if entry == "observe" {
			observes++
		}
	}
	if observes != 1 {
		t.Fatalf("fixer re-observed PR: %v", log.items)
	}
}

func TestReconcileMergeNotifiesParentAfterMerge(t *testing.T) {
	r, store, scm, h, _ := newReconciler(t, core.VerdictMerge,
		core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open"})
	run := store.runs["r1"]
	run.ParentSessionID = "ses_parent"
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	log := &callLog{}
	scm.log = log
	h.log = log

	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	mergeAt, notifyAt := -1, -1
	for i, entry := range log.items {
		switch entry {
		case "merge":
			if mergeAt < 0 {
				mergeAt = i
			}
		case "notify_parent":
			if notifyAt < 0 {
				notifyAt = i
			}
		}
	}
	if mergeAt < 0 || notifyAt < 0 || notifyAt <= mergeAt {
		t.Fatalf("notification must follow merge: %v", log.items)
	}
	if len(h.notifications) != 1 {
		t.Fatalf("notifications = %d, want 1", len(h.notifications))
	}
	got := h.notifications[0]
	if got.sessionID != "ses_parent" {
		t.Errorf("sessionID = %q, want ses_parent", got.sessionID)
	}
	for _, want := range []string{"42", "r1", "merged"} {
		if !strings.Contains(got.text, want) {
			t.Errorf("notification text %q missing %q", got.text, want)
		}
	}
}

func TestReconcileMergeWithoutParentSkipsNotification(t *testing.T) {
	r, _, _, h, _ := newReconciler(t, core.VerdictMerge,
		core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open"})
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if len(h.notifications) != 0 {
		t.Fatalf("notifications = %d, want 0", len(h.notifications))
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

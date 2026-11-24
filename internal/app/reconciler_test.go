package app

import (
	"context"
	"errors"
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

// behindAwarePolicy mirrors the production policy's behind invariant. A behind
// observation returns needs_rebase; the same observation without behind passes
// all criteria and returns merge.
type behindAwarePolicy struct{}

func (behindAwarePolicy) Evaluate(o core.Observation) core.MergeDecision {
	return core.Decide(o, nil)
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

func TestIndependentBaseChangeDoesNotConsumeRebaseAttempt(t *testing.T) {
	r, store, scm, _, _ := newReconciler(t, core.VerdictNeedsRebase,
		core.Observation{
			PRNumber: 42, HeadSHA: "sha1", BaseSHA: "base-new", PRState: "open", Behind: true,
			Checks: []core.CheckResult{{Name: "ci", State: core.CheckSuccess}},
		})
	run := store.runs["r1"]
	run.Scope = []core.ScopePath{core.ScopeFile("internal/app/isolated.go")}
	run.ObservedSHA = "sha1"
	run.ObservedBaseSHA = "base-old"
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 4; i++ {
		if _, err := r.Reconcile(context.Background(), 42); err != nil {
			t.Fatal(err)
		}
	}
	if scm.rebases != 0 {
		t.Fatalf("branch updates = %d, want 0", scm.rebases)
	}
	got := store.runs["r1"]
	if got.RebaseAttempts != 0 {
		t.Fatalf("RebaseAttempts = %d, want 0", got.RebaseAttempts)
	}
	if got.State != core.RunWaitingCI {
		t.Fatalf("state = %s, want waiting_ci", got.State)
	}
}

func TestRedPRWithNewBaseStillRebases(t *testing.T) {
	r, _, scm, _, _ := newReconciler(t, core.VerdictNeedsRebase,
		core.Observation{
			PRNumber: 42, HeadSHA: "sha1", BaseSHA: "base-new", PRState: "open", Behind: true,
			Checks: []core.CheckResult{{Name: "ci", State: core.CheckFailure}},
		})
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if scm.rebases != 1 {
		t.Fatalf("branch updates = %d, want 1", scm.rebases)
	}
}

func TestConflictWithNeedsRebaseStillRebases(t *testing.T) {
	r, _, scm, _, _ := newReconciler(t, core.VerdictNeedsRebase,
		core.Observation{
			PRNumber: 42, HeadSHA: "sha1", BaseSHA: "base-new", PRState: "open", Behind: true, Conflict: true,
			Checks: []core.CheckResult{{Name: "ci", State: core.CheckSuccess}},
		})
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if scm.rebases != 1 {
		t.Fatalf("branch updates = %d, want 1", scm.rebases)
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

// changingSCM reports a PR head that advances only when UpdateBranch succeeds.
// A failed update leaves the observed head unchanged.
type changingSCM struct {
	*fakeSCM
	head    string
	updates int
}

func newChangingSCM(head string) *changingSCM {
	return &changingSCM{
		fakeSCM: &fakeSCM{obs: core.Observation{PRNumber: 42, HeadSHA: head, PRState: "open"}},
		head:    head,
	}
}

func (f *changingSCM) Observe(_ context.Context, pr int) (core.Observation, error) {
	return core.Observation{PRNumber: pr, HeadSHA: f.head, PRState: "open", Behind: true}, nil
}

func (f *changingSCM) UpdateBranch(_ context.Context, _ int, sha string) (string, error) {
	f.updates++
	if f.updateErr != nil {
		return "", f.updateErr
	}
	f.head = "new-" + sha
	return f.head, nil
}

func TestRebaseOneHeadAdvanceCausesOneRebase(t *testing.T) {
	r, _, _, _, _ := newReconciler(t, core.VerdictNeedsRebase,
		core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open", Behind: true})
	scm := newChangingSCM("sha2")
	r.SCM = scm

	for i := 0; i < 4; i++ {
		if _, err := r.Reconcile(context.Background(), 42); err != nil {
			t.Fatal(err)
		}
	}
	if scm.updates != 1 {
		t.Fatalf("branch updates = %d, want 1", scm.updates)
	}
}

func TestRebaseSuccessMarksNewHead(t *testing.T) {
	r, store, _, _, _ := newReconciler(t, core.VerdictNeedsRebase,
		core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open", Behind: true})
	scm := newChangingSCM("sha2")
	r.SCM = scm

	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	got := store.runs["r1"]
	if got.HeadSHA != "new-sha2" {
		t.Fatalf("HeadSHA = %q, want new-sha2", got.HeadSHA)
	}
	if got.ObservedSHA != "new-sha2" {
		t.Fatalf("ObservedSHA = %q, want new-sha2", got.ObservedSHA)
	}
}

func TestRebaseFailureDoesNotRetrySameSHA(t *testing.T) {
	r, store, _, h, _ := newReconciler(t, core.VerdictNeedsRebase,
		core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open", Behind: true})
	scm := newChangingSCM("sha2")
	scm.updateErr = errors.New("rebase conflict")
	r.SCM = scm

	for i := 0; i < 5; i++ {
		if _, err := r.Reconcile(context.Background(), 42); err != nil {
			t.Fatal(err)
		}
	}
	if scm.updates != 1 {
		t.Fatalf("branch updates = %d, want 1", scm.updates)
	}
	if len(h.wakes) != 0 {
		t.Fatalf("agent woken %d times after failed rebase", len(h.wakes))
	}
	if got := store.runs["r1"]; got.ObservedSHA != "sha2" {
		t.Fatalf("ObservedSHA = %q, want sha2", got.ObservedSHA)
	}
}

func TestWaitingRunAdvancesObservedSHA(t *testing.T) {
	r, store, _, _, _ := newReconciler(t, core.VerdictWait,
		core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open"})
	scm := newChangingSCM("sha2")
	r.SCM = scm

	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	got := store.runs["r1"]
	if got.State != core.RunWaitingCI {
		t.Fatalf("state = %s, want waiting_ci", got.State)
	}
	if got.ObservedSHA != "sha2" {
		t.Fatalf("ObservedSHA = %q, want sha2", got.ObservedSHA)
	}
}

func TestRebaseNewBaseSHAWithFixedHeadRebasesOnce(t *testing.T) {
	r, store, _, _, _ := newReconciler(t, core.VerdictNeedsRebase, core.Observation{})
	run := store.runs["r1"]
	run.ObservedSHA = "sha1"
	run.ObservedBaseSHA = "base-old"
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	scm := newBaseMovingSCM("sha1", "base-new")
	r.SCM = scm

	for i := 0; i < 4; i++ {
		if _, err := r.Reconcile(context.Background(), 42); err != nil {
			t.Fatal(err)
		}
	}
	if scm.updates != 1 {
		t.Fatalf("branch updates = %d, want 1", scm.updates)
	}
	got := store.runs["r1"]
	if got.ObservedBaseSHA != "base-new" {
		t.Fatalf("ObservedBaseSHA = %q, want base-new", got.ObservedBaseSHA)
	}
	if got.ObservedSHA != "new-sha1" {
		t.Fatalf("ObservedSHA = %q, want new-sha1", got.ObservedSHA)
	}
}

func TestWakeNewBaseSHAWithFixedHeadWakesOnce(t *testing.T) {
	r, store, _, h, _ := newReconciler(t, core.VerdictNeedsAgentFix,
		core.Observation{PRNumber: 42, HeadSHA: "sha1", BaseSHA: "base-new", PRState: "open", FailureExcerpt: "panic: boom"})
	run := store.runs["r1"]
	run.ObservedSHA = "sha1"
	run.ObservedBaseSHA = "base-old"
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 4; i++ {
		if _, err := r.Reconcile(context.Background(), 42); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.wakes) != 1 {
		t.Fatalf("agent wakes = %d, want 1", len(h.wakes))
	}
	got := store.runs["r1"]
	if got.ObservedBaseSHA != "base-new" {
		t.Fatalf("ObservedBaseSHA = %q, want base-new", got.ObservedBaseSHA)
	}
}

func TestBehindGreenPRMergesWhenMergeable(t *testing.T) {
	r, store, scm, _, _ := newReconciler(t, core.VerdictNeedsRebase,
		core.Observation{
			PRNumber: 42, HeadSHA: "sha2", BaseSHA: "base-new", PRState: "open", Behind: true,
			Checks: []core.CheckResult{{Name: "ci", State: core.CheckSuccess}},
		})
	r.Policy = behindAwarePolicy{}
	run := store.runs["r1"]
	run.ObservedSHA = "sha1"
	run.ObservedBaseSHA = "base-old"
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	decision, err := r.Reconcile(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != core.VerdictMerge {
		t.Fatalf("verdict = %s, want merge", decision.Verdict)
	}
	got := store.runs["r1"]
	if got.State != core.RunMerged || scm.merges != 1 {
		t.Fatalf("state=%s merges=%d", got.State, scm.merges)
	}
	if got.ObservedSHA != "sha2" || got.ObservedBaseSHA != "base-new" {
		t.Fatalf("observation not refreshed on merge: sha=%q base=%q", got.ObservedSHA, got.ObservedBaseSHA)
	}
	if scm.rebases != 0 {
		t.Fatalf("branch updates = %d, want 0", scm.rebases)
	}
}

func TestGreenBehindNeedsRebaseLeavesStuckState(t *testing.T) {
	r, store, scm, _, _ := newReconciler(t, core.VerdictNeedsRebase,
		core.Observation{
			PRNumber: 42, HeadSHA: "sha2", BaseSHA: "base-new", PRState: "open", Behind: true,
			Checks: []core.CheckResult{{Name: "ci", State: core.CheckSuccess}},
		})
	run := store.runs["r1"]
	run.State = core.RunNeedsRebase
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	got := store.runs["r1"]
	if got.State != core.RunWaitingCI {
		t.Fatalf("state = %s, want waiting_ci", got.State)
	}
	if scm.rebases != 0 || scm.merges != 0 {
		t.Fatalf("rebases=%d merges=%d, want pure state transition", scm.rebases, scm.merges)
	}
}

func TestBehindGreenPRWithConflictDoesNotMerge(t *testing.T) {
	r, store, scm, h, _ := newReconciler(t, core.VerdictNeedsAgentFix,
		core.Observation{
			PRNumber: 42, HeadSHA: "sha2", BaseSHA: "base-new", PRState: "open", Behind: true, Conflict: true,
			Checks: []core.CheckResult{{Name: "ci", State: core.CheckSuccess}},
		})
	r.Policy = behindAwarePolicy{}

	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if scm.merges != 0 {
		t.Fatalf("merges = %d, want 0 for a conflicting PR", scm.merges)
	}
	if len(h.wakes) == 0 && scm.rebases == 0 {
		t.Fatal("real conflict took neither the fix nor the rebase path")
	}
	if got := store.runs["r1"]; got.State == core.RunMerged {
		t.Fatal("conflicting PR reached merged")
	}
}

func epicSource(review core.ReviewMode) *fakeSource {
	return &fakeSource{tasks: []core.Task{
		{ID: "T-EPIC", Title: "Big epic", Kind: core.TaskKindEpic, Status: core.TaskAvailable,
			BaseBranch: "main", RequireReview: review},
		{ID: "T-EPIC-01", Title: "Child one", Kind: core.TaskKindTask, Status: core.TaskAvailable,
			BaseBranch: "beacon/T-EPIC"},
	}}
}

func epicRun(t *testing.T, store *fakeStore, id core.RunID, pr int) {
	t.Helper()
	run := makeRun(id, pr, core.RunWaitingCI)
	run.TaskID = "T-EPIC"
	run.Branch = "beacon/T-EPIC"
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
}

func TestChildPRMergesIntoEpicBranchAndOpensEpicPR(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	child := makeRun("child-1", 42, core.RunWaitingCI)
	child.TaskID = "T-EPIC-01"
	child.Branch = "beacon/T-EPIC-01"
	if err := store.Save(ctx, child); err != nil {
		t.Fatal(err)
	}
	store.ready = []core.Task{{ID: "T-EPIC", Title: "Big epic", Kind: core.TaskKindEpic}}
	scm := &fakeSCM{
		obs:     core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open"},
		foundPR: 55,
	}
	r := &Reconciler{
		Store: store, SCM: scm,
		Policy:  fakePolicy{decision: core.MergeDecision{Verdict: core.VerdictMerge}},
		Harness: &fakeHarness{}, Source: epicSource(core.ReviewModeLLM),
		Workspace: &fakeWorkspace{}, BaseBranch: "main",
	}

	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if got := store.runs["child-1"]; got.State != core.RunMerged {
		t.Fatalf("child state = %s, want merged", got.State)
	}
	if store.taskStatus["T-EPIC-01"] != "done" {
		t.Fatalf("child task status = %q, want done", store.taskStatus["T-EPIC-01"])
	}
	if len(scm.openedPRs) != 1 {
		t.Fatalf("epic PRs opened = %d, want 1", len(scm.openedPRs))
	}
	want := openedPR{head: "beacon/T-EPIC", base: "main", title: "Big epic"}
	if scm.openedPRs[0] != want {
		t.Fatalf("epic PR = %+v, want %+v", scm.openedPRs[0], want)
	}
	epic := store.runs["epic-T-EPIC"]
	if epic == nil || epic.PRNumber != 55 || epic.State != core.RunWaitingCI {
		t.Fatalf("epic run = %+v, want waiting_ci on PR 55", epic)
	}
}

func TestEpicPROpensOnceWhenChildrenDone(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.ready = []core.Task{{ID: "T-EPIC", Title: "Big epic", Kind: core.TaskKindEpic}}
	scm := &fakeSCM{foundPR: 55}
	r := &Reconciler{
		Store: store, SCM: scm, Harness: &fakeHarness{},
		Source: epicSource(core.ReviewModeNone), Workspace: &fakeWorkspace{}, BaseBranch: "main",
	}

	if err := r.reconcileEpics(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileEpics(ctx); err != nil {
		t.Fatal(err)
	}
	if len(scm.openedPRs) != 1 {
		t.Fatalf("epic PRs opened = %d, want 1", len(scm.openedPRs))
	}
}

func TestEpicLLMReviewIsDefaultAndAutoMergesOnApproval(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	epicRun(t, store, "epic-1", 55)
	scm := &fakeSCM{obs: core.Observation{PRNumber: 55, HeadSHA: "sha1", PRState: "open"}}
	h := &fakeHarness{}
	r := &Reconciler{
		Store: store, SCM: scm,
		Policy:  fakePolicy{decision: core.MergeDecision{Verdict: core.VerdictMerge}},
		Harness: h, Source: epicSource(""),
		Workspace: &fakeWorkspace{}, BaseBranch: "main",
	}

	decision, err := r.Reconcile(ctx, 55)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != core.VerdictWait || scm.merges != 0 {
		t.Fatalf("decision=%+v merges=%d, want wait with no merge", decision, scm.merges)
	}
	if len(h.sessions) != 1 {
		t.Fatalf("reviewer sessions = %d, want 1", len(h.sessions))
	}
	if !strings.Contains(h.prompts[0], "55") || !strings.Contains(h.prompts[0], "sha1") {
		t.Fatalf("review prompt = %q, want PR and SHA", h.prompts[0])
	}

	if err := store.SetApproval(ctx, "epic-1", "sha1"); err != nil {
		t.Fatal(err)
	}
	decision, err = r.Reconcile(ctx, 55)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != core.VerdictMerge || scm.merges != 1 {
		t.Fatalf("decision=%+v merges=%d, want merge", decision, scm.merges)
	}
	if len(h.sessions) != 1 {
		t.Fatalf("reviewer restarted on approval: %d", len(h.sessions))
	}
}

func TestEpicHumanReviewWaitsForManualApproval(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	epicRun(t, store, "epic-1", 55)
	scm := &fakeSCM{obs: core.Observation{PRNumber: 55, HeadSHA: "sha1", PRState: "open"}}
	h := &fakeHarness{}
	r := &Reconciler{
		Store: store, SCM: scm,
		Policy:  fakePolicy{decision: core.MergeDecision{Verdict: core.VerdictMerge}},
		Harness: h, Source: epicSource(core.ReviewModeHuman),
		Workspace: &fakeWorkspace{}, BaseBranch: "main",
	}

	decision, err := r.Reconcile(ctx, 55)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != core.VerdictWait || scm.merges != 0 {
		t.Fatalf("decision=%+v merges=%d, human gate did not hold", decision, scm.merges)
	}
	if len(h.sessions) != 0 {
		t.Fatalf("human gate spawned %d reviewers", len(h.sessions))
	}
	if err := store.SetApproval(ctx, "epic-1", "sha1"); err != nil {
		t.Fatal(err)
	}
	decision, err = r.Reconcile(ctx, 55)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != core.VerdictMerge || scm.merges != 1 {
		t.Fatalf("decision=%+v merges=%d, want merge after approval", decision, scm.merges)
	}
}

func TestEpicReviewNoneSkipsGate(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	epicRun(t, store, "epic-1", 55)
	scm := &fakeSCM{obs: core.Observation{PRNumber: 55, HeadSHA: "sha1", PRState: "open"}}
	h := &fakeHarness{}
	r := &Reconciler{
		Store: store, SCM: scm,
		Policy:  fakePolicy{decision: core.MergeDecision{Verdict: core.VerdictMerge}},
		Harness: h, Source: epicSource(core.ReviewModeNone),
		Workspace: &fakeWorkspace{}, BaseBranch: "main",
	}

	decision, err := r.Reconcile(ctx, 55)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != core.VerdictMerge || scm.merges != 1 {
		t.Fatalf("decision=%+v merges=%d, want immediate merge", decision, scm.merges)
	}
	if len(h.sessions) != 0 {
		t.Fatalf("none mode spawned %d reviewers", len(h.sessions))
	}
}

func TestNeedsAgentFixWithGreenChecksAdvancesToMerged(t *testing.T) {
	r, store, _, _, _ := newReconciler(t, core.VerdictNeedsRebase,
		core.Observation{
			PRNumber: 42, HeadSHA: "sha2", BaseSHA: "base-new", PRState: "open", Behind: true,
			Checks: []core.CheckResult{{Name: "ci", State: core.CheckSuccess}},
		})
	run := store.runs["r1"]
	run.State = core.RunNeedsAgentFix
	run.HeadSHA = "sha1"
	run.ObservedSHA = "sha1"
	run.ObservedBaseSHA = "base-old"
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	got := store.runs["r1"]
	if got.State != core.RunWaitingCI {
		t.Fatalf("state = %s, want waiting_ci", got.State)
	}
	if got.HeadSHA != "sha2" {
		t.Fatalf("HeadSHA = %q, want sha2", got.HeadSHA)
	}
	if got.ObservedSHA != "sha2" {
		t.Fatalf("ObservedSHA = %q, want sha2", got.ObservedSHA)
	}

	r.Policy = fakePolicy{decision: core.MergeDecision{Verdict: core.VerdictMerge, Reason: "all criteria passed"}}
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if got = store.runs["r1"]; got.State != core.RunMerged {
		t.Fatalf("state = %s, want merged", got.State)
	}
}

func TestMergeWithEmptyWorkspacePathStillMerges(t *testing.T) {
	r, store, scm, _, src := newReconciler(t, core.VerdictMerge,
		core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open"})
	run := store.runs["r1"]
	run.WorkspacePath = ""
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
	got := store.runs["r1"]
	if got.State != core.RunMerged || scm.merges != 1 {
		t.Fatalf("state=%s merges=%d", got.State, scm.merges)
	}
	if len(src.marked) != 0 || len(ws.commits) != 0 {
		t.Fatalf("empty path marked or committed card: marked=%+v commits=%+v", src.marked, ws.commits)
	}
	if len(src.complete) != 1 || src.complete[0] != run.TaskID {
		t.Fatalf("task not completed after merge: %+v", src.complete)
	}
}

func TestMergeRetriesNonFastForwardPushFromRemoteBranch(t *testing.T) {
	r, store, scm, _, _ := newReconciler(t, core.VerdictMerge,
		core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open"})
	run := store.runs["r1"]
	run.Branch = "beacon/T-1"
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	scm.observeSeq = []core.Observation{
		{PRNumber: 42, HeadSHA: "sha2", PRState: "open"},
		{PRNumber: 42, HeadSHA: "fresh-sha", PRState: "open"},
	}
	log := &callLog{}
	scm.log = log
	ws := &fakeWorkspace{log: log, rejectFirst: true}
	r.Workspace = ws

	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	got := store.runs["r1"]
	if got.State != core.RunMerged || scm.merges != 1 {
		t.Fatalf("state=%s merges=%d", got.State, scm.merges)
	}
	if ws.pushes != 2 {
		t.Fatalf("push attempts = %d, want 2", ws.pushes)
	}
	if len(ws.rebases) != 1 || ws.rebases[0] != "beacon/T-1" {
		t.Fatalf("rebases = %v, want one refresh onto the remote branch", ws.rebases)
	}
	if scm.mergeSHA != "fresh-sha" {
		t.Fatalf("merge SHA = %q, want fresh-sha", scm.mergeSHA)
	}
}

func TestMergeWithFailedRefreshWarnsAndStillMerges(t *testing.T) {
	r, store, scm, _, _ := newReconciler(t, core.VerdictMerge,
		core.Observation{PRNumber: 42, HeadSHA: "sha2", BaseSHA: "base1", PRState: "open"})
	run := store.runs["r1"]
	run.Branch = "beacon/T-1"
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	log := &callLog{}
	scm.log = log
	ws := &fakeWorkspace{log: log, rejectFirst: true, rebaseBaseErr: errors.New("fetch failed")}
	r.Workspace = ws
	logs := captureDecisions(t)

	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	got := store.runs["r1"]
	if got.State != core.RunMerged || scm.merges != 1 {
		t.Fatalf("state=%s merges=%d", got.State, scm.merges)
	}
	if ws.pushes != 1 {
		t.Fatalf("push attempts = %d, want 1 after a failed refresh", ws.pushes)
	}
	decisionRecord(t, logs, "card publish failed before merge")
	if got.ObservedSHA != "sha2" || got.ObservedBaseSHA != "base1" {
		t.Fatalf("observation not persisted: sha=%q base=%q", got.ObservedSHA, got.ObservedBaseSHA)
	}
}

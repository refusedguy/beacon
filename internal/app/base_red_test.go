package app

import (
	"context"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func redBase() core.BaseStatus {
	return core.BaseStatus{
		BaseBranch: "main", HeadSHA: "base1",
		FailedChecks:   []core.CheckResult{{Name: "ci", State: core.CheckFailure, Output: "test failed"}},
		FailureExcerpt: "test failed",
	}
}

func redReconciler(t *testing.T) (*Reconciler, *fakeStore, *fakeSCM, *fakeHarness, chan int) {
	t.Helper()
	store := newFakeStore()
	run := makeRun("r1", 42, core.RunWaitingCI)
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	changes := make(chan int, 4)
	scm := &fakeSCM{
		obs: core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open",
			Checks: []core.CheckResult{{Name: "ci", State: core.CheckFailure}}, FailureExcerpt: "test failed"},
		base: redBase(),
	}
	h := &fakeHarness{}
	return &Reconciler{
		Store: store, SCM: scm, Policy: fakePolicy{decision: core.MergeDecision{Verdict: core.VerdictNeedsAgentFix, Reason: "ci failed"}},
		Harness: h, Source: &fakeSource{}, Workspace: &fakeWorkspace{}, Changes: changes,
		Repo: "o/r", BaseBranch: "main", BaseRedMode: "spawn_fixer", MaxFixTries: 2,
	}, store, scm, h, changes
}

func TestRedBaseSpawnsOneFixerAndRecovers(t *testing.T) {
	r, store, scm, h, _ := redReconciler(t)
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if store.runs["r1"].State != core.RunBlocked {
		t.Fatalf("run not blocked: %s", store.runs["r1"].State)
	}
	fixers := 0
	for _, run := range store.runs {
		if run.IsFixer {
			fixers++
		}
	}
	if fixers != 1 {
		t.Fatalf("spawned %d fixers", fixers)
	}
	if len(h.wakes) != 0 {
		t.Fatalf("workers should not be woken on a red base: %d", len(h.wakes))
	}
	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatal(err)
	}
	fixers = 0
	for _, run := range store.runs {
		if run.IsFixer {
			fixers++
		}
	}
	if fixers != 1 {
		t.Fatalf("second reconcile spawned extra fixer: %d", fixers)
	}

	scm.base = core.BaseStatus{BaseBranch: "main", HeadSHA: "base2"}
	if err := r.checkBaseRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	if store.runs["r1"].State != core.RunWaitingCI {
		t.Fatalf("run not recovered: %s", store.runs["r1"].State)
	}
	if scm.rebases != 1 {
		t.Fatalf("expected one update-branch, got %d", scm.rebases)
	}
	incidents, _ := store.ListOpenIncidents(ctx)
	if len(incidents) != 0 {
		t.Fatalf("incident not resolved: %+v", incidents)
	}
}

func TestRedEpicBranchBlocksChildrenAndSpawnsFixer(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	child := makeRun("child-1", 42, core.RunWaitingCI)
	child.TaskID = "T-EPIC-01"
	child.Branch = "beacon/T-EPIC-01"
	if err := store.Save(ctx, child); err != nil {
		t.Fatal(err)
	}
	scm := &fakeSCM{
		obs: core.Observation{PRNumber: 42, HeadSHA: "sha2", PRState: "open",
			Checks:         []core.CheckResult{{Name: "ci", State: core.CheckFailure}},
			FailureExcerpt: "test failed"},
		base: core.BaseStatus{BaseBranch: "beacon/T-EPIC", HeadSHA: "base1",
			FailedChecks: []core.CheckResult{{Name: "ci", State: core.CheckFailure, Output: "test failed"}}},
	}
	h := &fakeHarness{}
	r := &Reconciler{
		Store: store, SCM: scm,
		Policy:  fakePolicy{decision: core.MergeDecision{Verdict: core.VerdictNeedsAgentFix, Reason: "ci failed"}},
		Harness: h, Source: epicSource(core.ReviewModeLLM),
		Workspace: &fakeWorkspace{}, Repo: "o/r", BaseBranch: "main",
		BaseRedMode: "spawn_fixer", MaxFixTries: 2,
	}

	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatal(err)
	}
	incidents, err := store.ListOpenIncidents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(incidents) != 1 {
		t.Fatalf("incidents = %d, want 1", len(incidents))
	}
	if incidents[0].BaseBranch != "beacon/T-EPIC" {
		t.Fatalf("incident base = %q, want beacon/T-EPIC", incidents[0].BaseBranch)
	}
	if got := store.runs["child-1"]; got.State != core.RunBlocked {
		t.Fatalf("child state = %s, want blocked", got.State)
	}
	fixers := 0
	for _, run := range store.runs {
		if run.IsFixer {
			fixers++
		}
	}
	if fixers != 1 {
		t.Fatalf("fixers = %d, want 1", fixers)
	}
	found := false
	for _, prompt := range h.prompts {
		if strings.Contains(prompt, "beacon/T-EPIC") {
			found = true
		}
	}
	if !found {
		t.Fatalf("fixer prompt missing epic branch: %v", h.prompts)
	}
}

func TestIdleAutoAttachesPRByBranch(t *testing.T) {
	store := newFakeStore()
	run := makeRun("r1", 0, core.RunRunning)
	run.Branch = "beacon/T-1"
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	changes := make(chan int, 4)
	r := &Reconciler{
		Store: store, SCM: &fakeSCM{foundPR: 42}, Harness: &fakeHarness{},
		Workspace: &fakeWorkspace{}, Changes: changes, BaseBranch: "main",
	}
	r.HandleHarnessEvent(context.Background(), core.HarnessEvent{RunID: "r1", Kind: "idle"})
	got := store.runs["r1"]
	if got.PRNumber != 42 || got.State != core.RunWaitingCI {
		t.Fatalf("auto attach failed: %+v", got)
	}
	select {
	case pr := <-changes:
		if pr != 42 {
			t.Fatalf("wrong pr signal: %d", pr)
		}
	default:
		t.Fatal("no pr change signal")
	}
}

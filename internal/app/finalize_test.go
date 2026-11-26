package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// failingOpenPRSCM makes epic PR publication fail after the delivery key is claimed.
type failingOpenPRSCM struct {
	*fakeSCM
	err error
}

func (f *failingOpenPRSCM) OpenPR(context.Context, string, string, string) (int, error) {
	return 0, f.err
}

func TestBaseBranchFollowsTaskParentNotIDShape(t *testing.T) {
	ctx := context.Background()
	r := &Reconciler{
		Source: &fakeSource{tasks: []core.Task{
			{ID: "T-EPIC", Kind: core.TaskKindEpic, Status: core.TaskAvailable},
			{ID: "T-CHILD", Parent: "T-EPIC", Status: core.TaskAvailable},
		}},
		BaseBranch: "main",
	}
	run := core.NewRun("r1", "T-CHILD", time.Now().UTC())

	if got := r.baseBranchFor(ctx, run); got != "beacon/T-EPIC" {
		t.Fatalf("base branch = %q, want beacon/T-EPIC", got)
	}
}

// countingSource records card source reads. A red-base incident must read the
// cards once no matter how many runs share the base.
type countingSource struct {
	*fakeSource
	lists int
}

func (s *countingSource) ListAvailable(ctx context.Context) ([]core.Task, error) {
	s.lists++
	return s.fakeSource.ListAvailable(ctx)
}

func redBaseTreeReconciler(t testing.TB, cards, runs int) (*Reconciler, *countingSource, *fakeStore) {
	t.Helper()
	ctx := context.Background()
	tasks := make([]core.Task, cards)
	for i := range tasks {
		tasks[i] = core.Task{ID: taskIDFor(i), Status: core.TaskAvailable}
	}
	source := &countingSource{fakeSource: &fakeSource{tasks: tasks}}
	store := newFakeStore()
	checks := []core.CheckResult{{Name: "ci", State: core.CheckFailure}}
	for i := 0; i < runs; i++ {
		run := makeRun(runIDFor(i), i+1, core.RunWaitingCI)
		run.TaskID = taskIDFor(i)
		if err := store.Save(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	scm := &fakeSCM{obs: core.Observation{Checks: checks}}
	return &Reconciler{Store: store, SCM: scm, Source: source, BaseBranch: "main"}, source, store
}

func taskIDFor(i int) core.TaskID { return core.TaskID(fmt.Sprintf("T-%04d", i)) }

func runIDFor(i int) core.RunID { return core.RunID(fmt.Sprintf("r-%04d", i)) }

// TestBaseBranchForTreeMatchesPerRunLookup pins the edge cases the cache must
// preserve: a duplicate ID resolves to the last card, and a missing card or
// parent falls back to the configured base.
func TestBaseBranchForTreeMatchesPerRunLookup(t *testing.T) {
	ctx := context.Background()
	r := &Reconciler{
		Source: &fakeSource{tasks: []core.Task{
			{ID: "T-EPIC", Kind: core.TaskKindEpic, Status: core.TaskAvailable},
			{ID: "T-DUP", Parent: "T-EPIC", Status: core.TaskAvailable},
			{ID: "T-DUP", Parent: "T-MISSING", Status: core.TaskAvailable},
			{ID: "T-EPIC2", Kind: core.TaskKindEpic, Status: core.TaskAvailable},
			{ID: "T-DUP2", Parent: "T-MISSING", Status: core.TaskAvailable},
			{ID: "T-DUP2", Parent: "T-EPIC2", Status: core.TaskAvailable},
			{ID: "T-ORPHAN", Parent: "T-NOPE", Status: core.TaskAvailable},
		}},
		BaseBranch: "main",
	}
	tree := r.taskTree(ctx)
	for _, tc := range []struct {
		id   core.TaskID
		want string
	}{
		{"T-DUP", "main"},
		{"T-DUP2", "beacon/T-EPIC2"},
		{"T-ORPHAN", "main"},
		{"T-ABSENT", "main"},
	} {
		run := core.NewRun(core.RunID("r-"+tc.id), tc.id, time.Now().UTC())
		got := r.baseBranchForTree(tree, run)
		if got != tc.want {
			t.Fatalf("task %s: baseBranchForTree = %q, want %q", tc.id, got, tc.want)
		}
		if perRun := r.baseBranchFor(ctx, run); perRun != got {
			t.Fatalf("task %s: baseBranchForTree = %q, baseBranchFor = %q", tc.id, got, perRun)
		}
	}
}

// TestBaseBranchForTreeKeepsEpicParent checks that the shared tree still finds
// an epic parent, so caching does not drop the epic branch behavior.
func TestBaseBranchForTreeKeepsEpicParent(t *testing.T) {
	ctx := context.Background()
	r := &Reconciler{
		Source: &fakeSource{tasks: []core.Task{
			{ID: "T-EPIC", Kind: core.TaskKindEpic, Status: core.TaskAvailable},
			{ID: "T-CHILD", Parent: "T-EPIC", Status: core.TaskAvailable},
		}},
		BaseBranch: "main",
	}
	tree := r.taskTree(ctx)
	run := core.NewRun("r1", "T-CHILD", time.Now().UTC())
	if got := r.baseBranchForTree(tree, run); got != "beacon/T-EPIC" {
		t.Fatalf("base branch = %q, want beacon/T-EPIC", got)
	}
}

func TestBlockRunsOnRedBaseReadsSourceOnce(t *testing.T) {
	ctx := context.Background()
	r, source, _ := redBaseTreeReconciler(t, 1000, 500)
	in := &core.BaseIncident{ID: "inc-1"}
	failed := []core.CheckResult{{Name: "ci", State: core.CheckFailure}}
	if err := r.blockRunsOnRedBaseNames(ctx, in, "main", failedNames(failed)); err != nil {
		t.Fatal(err)
	}
	if source.lists != 1 {
		t.Fatalf("ListAvailable called %d times, want 1", source.lists)
	}
}

// BenchmarkBlockRunsOnRedBaseOneTreeBuild runs one red-base incident over 1000
// cards and 500 waiting_ci runs. One ListAvailable call also means one tree
// build, since each taskTree read builds exactly one tree.
func BenchmarkBlockRunsOnRedBaseOneTreeBuild(b *testing.B) {
	ctx := context.Background()
	r, source, store := redBaseTreeReconciler(b, 1000, 500)
	in := &core.BaseIncident{ID: "inc-bench"}
	failed := []core.CheckResult{{Name: "ci", State: core.CheckFailure}}
	for i := 0; i < b.N; i++ {
		for _, run := range store.runs {
			run.State = core.RunWaitingCI
			run.BlockedIncidentID = ""
		}
		source.lists = 0
		if err := r.blockRunsOnRedBaseNames(ctx, in, "main", failedNames(failed)); err != nil {
			b.Fatal(err)
		}
		if source.lists != 1 {
			b.Fatalf("ListAvailable called %d times, want 1", source.lists)
		}
	}
}

func TestEpicPRDeliveryReleasedWhenOpenFails(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.ready = []core.Task{{ID: "T-EPIC", Title: "Big epic", Kind: core.TaskKindEpic}}
	r := &Reconciler{
		Store: store, SCM: &failingOpenPRSCM{fakeSCM: &fakeSCM{}, err: errors.New("open failed")},
		Harness: &fakeHarness{}, Source: epicSource(core.ReviewModeNone),
		Workspace: &fakeWorkspace{}, BaseBranch: "main",
	}

	if err := r.reconcileEpics(ctx); err != nil {
		t.Fatal(err)
	}
	key := "epic-pr:T-EPIC"
	if store.deliveries[key] {
		t.Fatal("epic delivery claim survived a failed PR open")
	}
	ok, err := store.ClaimDelivery(ctx, key)
	if err != nil || !ok {
		t.Fatalf("later tick cannot reclaim epic delivery: ok=%v err=%v", ok, err)
	}
}

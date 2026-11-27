package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

func TestSyncTasksKeepsBeaconDoneWhenFileIsStale(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	initial := []core.Task{
		{ID: "T-1", Status: core.TaskDone},
		{ID: "T-2", Status: core.TaskAvailable, DependsOn: []core.TaskID{"T-1"}},
	}
	if err := store.SyncTasks(ctx, initial); err != nil {
		t.Fatal(err)
	}
	// A stale working tree still reports T-1 as available. Beacon's done
	// status must survive the sync so T-2 stays unblocked.
	stale := []core.Task{
		{ID: "T-1", Status: core.TaskAvailable},
		{ID: "T-2", Status: core.TaskAvailable, DependsOn: []core.TaskID{"T-1"}},
	}
	if err := store.SyncTasks(ctx, stale); err != nil {
		t.Fatal(err)
	}
	ready, err := store.ListReadyTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, task := range ready {
		if task.ID == "T-2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("T-2 stayed blocked by a reverted card: %+v", ready)
	}
}

func TestSyncTasksMergesAndDropsAbsentRows(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SyncTasks(ctx, []core.Task{
		{ID: "T-1", Status: core.TaskAvailable},
		{ID: "T-2", Status: core.TaskAvailable, DependsOn: []core.TaskID{"T-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncTasks(ctx, []core.Task{{ID: "T-3", Status: core.TaskAvailable}}); err != nil {
		t.Fatal(err)
	}
	ready, err := store.ListReadyTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].ID != "T-3" {
		t.Fatalf("ready = %+v, want only T-3", ready)
	}
}

func TestListQueueTasksIgnoresMissingDependency(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	// Insert a dependency edge whose target card has been deleted. The queue
	// must not show the dependent task as waiting on a phantom blocker.
	if err := store.SyncTasks(ctx, []core.Task{
		{ID: "T-1", Status: core.TaskAvailable, DependsOn: []core.TaskID{"T-GONE"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO task_deps(task_id, depends_on) VALUES ('T-1','T-GONE')`); err != nil {
		t.Fatal(err)
	}
	var queue []app.QueueTask
	queue, err = store.ListQueueTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 1 || queue[0].Waiting {
		t.Fatalf("queue = %+v, want T-1 without waiting_deps", queue)
	}
}

package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func statusOf(t *testing.T, s *Store, id string) string {
	t.Helper()
	var status string
	if err := s.db.QueryRowContext(context.Background(), `SELECT status FROM tasks WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestSyncTasksKeepsBeaconClaimedStatus(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	initial := []core.Task{
		{ID: "T-1", Status: core.TaskAvailable},
		{ID: "T-2", Status: core.TaskAvailable, DependsOn: []core.TaskID{"T-1"}},
	}
	if err := store.SyncTasks(ctx, initial); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskStatus(ctx, "T-1", string(core.TaskClaimed)); err != nil {
		t.Fatal(err)
	}
	// The working tree still reports the pre-claim status. The claim must
	// survive the sync, just as a done status does.
	stale := []core.Task{
		{ID: "T-1", Status: core.TaskAvailable},
		{ID: "T-2", Status: core.TaskAvailable, DependsOn: []core.TaskID{"T-1"}},
	}
	if err := store.SyncTasks(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, store, "T-1"); got != string(core.TaskClaimed) {
		t.Fatalf("status = %q, want %q", got, core.TaskClaimed)
	}
	ready, err := store.ListReadyTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range ready {
		if task.ID == "T-2" {
			t.Fatalf("T-2 became ready while its dependency is claimed: %+v", ready)
		}
	}
}

func TestSyncTasksRejectsDuplicateIDs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SyncTasks(ctx, []core.Task{{ID: "T-1", Status: core.TaskAvailable}}); err != nil {
		t.Fatal(err)
	}
	dupes := []core.Task{
		{ID: "T-2", Status: core.TaskAvailable},
		{ID: "T-2", Status: core.TaskDone},
	}
	if code := core.CodeOf(store.SyncTasks(ctx, dupes)); code != core.CodeInvalidInput {
		t.Fatalf("code = %q, want %q", code, core.CodeInvalidInput)
	}
	ready, err := store.ListReadyTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].ID != "T-1" {
		t.Fatalf("graph changed after rejected sync: %+v", ready)
	}
}

func TestReadyAndQueueAgreeOnDanglingDependency(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SyncTasks(ctx, []core.Task{
		{ID: "T-1", Status: core.TaskAvailable, DependsOn: []core.TaskID{"T-GONE"}},
		{ID: "T-2", Status: core.TaskAvailable, DependsOn: []core.TaskID{"T-HOLD"}},
		{ID: "T-HOLD", Status: core.TaskAvailable},
	}); err != nil {
		t.Fatal(err)
	}
	// Recreate the dangling edge after the sync pruned it.
	if _, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO task_deps(task_id, depends_on) VALUES ('T-1','T-GONE')`); err != nil {
		t.Fatal(err)
	}
	ready, err := store.ListReadyTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	readySet := map[string]bool{}
	for _, task := range ready {
		readySet[string(task.ID)] = true
	}
	if !readySet["T-1"] || readySet["T-2"] {
		t.Fatalf("ready = %+v, want T-1 only", ready)
	}
	queue, err := store.ListQueueTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waiting := map[string]bool{}
	for _, task := range queue {
		waiting[task.ID] = task.Waiting
	}
	if waiting["T-1"] || !waiting["T-2"] {
		t.Fatalf("queue waiting = %v, want only T-2", waiting)
	}
}

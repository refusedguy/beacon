package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestSyncTasksPersistsParent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	tasks := []core.Task{
		{ID: "T-EPIC", Kind: core.TaskKindEpic, Status: core.TaskAvailable},
		{ID: "T-CHILD", Parent: "T-EPIC", Status: core.TaskAvailable},
	}
	if err := store.SyncTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	ready, err := store.ListReadyTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 2 {
		t.Fatalf("ready tasks = %+v", ready)
	}
	byID := make(map[core.TaskID]core.Task, len(ready))
	for _, task := range ready {
		byID[task.ID] = task
	}
	if got := byID["T-CHILD"].Parent; got != "T-EPIC" {
		t.Fatalf("child parent = %q, want T-EPIC", got)
	}
	if got := byID["T-EPIC"].Parent; got != "" {
		t.Fatalf("epic parent = %q, want empty", got)
	}
}

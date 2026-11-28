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

func TestSyncTasksKeepsDoneCardThroughPruneAndStaleSource(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if err := store.SyncTasks(ctx, []core.Task{
		{ID: "T-DONE", Status: core.TaskAvailable},
		{ID: "T-OPEN", Status: core.TaskAvailable},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskStatus(ctx, "T-DONE", string(core.TaskDone)); err != nil {
		t.Fatal(err)
	}

	// The source lists only available cards, so T-DONE is absent. The prune
	// must keep its done row instead of deleting it.
	if err := store.SyncTasks(ctx, []core.Task{{ID: "T-OPEN", Status: core.TaskAvailable}}); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, store, "T-DONE"); got != string(core.TaskDone) {
		t.Fatalf("status after prune sync = %q, want %q", got, core.TaskDone)
	}

	// A later sync where a stale frontmatter lists T-DONE as available must
	// not revive it into the ready set, however often that sync runs.
	for i := 0; i < 2; i++ {
		if err := store.SyncTasks(ctx, []core.Task{
			{ID: "T-DONE", Status: core.TaskAvailable},
			{ID: "T-OPEN", Status: core.TaskAvailable},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := statusOf(t, store, "T-DONE"); got != string(core.TaskDone) {
		t.Fatalf("status after stale sync = %q, want %q", got, core.TaskDone)
	}
	ready, err := store.ListReadyTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].ID != "T-OPEN" {
		t.Fatalf("ready tasks = %+v, want only T-OPEN", ready)
	}
}

func TestSyncTasksRejectsBadGraphBeforeWriting(t *testing.T) {
	cases := []struct {
		name  string
		tasks []core.Task
	}{
		{
			name: "self dependency",
			tasks: []core.Task{
				{ID: "T-A", DependsOn: []core.TaskID{"T-A"}, Status: core.TaskAvailable},
			},
		},
		{
			name: "cycle",
			tasks: []core.Task{
				{ID: "T-A", DependsOn: []core.TaskID{"T-B"}, Status: core.TaskAvailable},
				{ID: "T-B", DependsOn: []core.TaskID{"T-A"}, Status: core.TaskAvailable},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()

			if err := store.SyncTasks(ctx, []core.Task{{ID: "T-A", Status: core.TaskAvailable}}); err != nil {
				t.Fatal(err)
			}
			err = store.SyncTasks(ctx, tc.tasks)
			if core.CodeOf(err) != core.CodeInvalidInput {
				t.Fatalf("error = %v, want CodeInvalidInput", err)
			}
			ready, err := store.ListReadyTasks(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(ready) != 1 || ready[0].ID != "T-A" {
				t.Fatalf("stored graph changed after a rejected sync: %+v", ready)
			}
		})
	}
}

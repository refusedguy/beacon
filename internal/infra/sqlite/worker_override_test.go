package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestWorkerOverrideRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	tasks := []core.Task{
		{ID: "T-WITH", Status: core.TaskAvailable, WorkerAgent: "table-driven",
			WorkerPrompt: "Use table-driven tests.", Scope: []core.ScopePath{core.ScopeFile("with.go")}},
		{ID: "T-WITHOUT", Status: core.TaskAvailable,
			Scope: []core.ScopePath{core.ScopeFile("without.go")}},
	}
	if err := store.SyncTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	ready, err := store.ListReadyTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[core.TaskID]core.Task{}
	for _, task := range ready {
		byID[task.ID] = task
	}
	if len(byID) != 2 {
		t.Fatalf("ready = %+v", ready)
	}
	with := byID["T-WITH"]
	if with.WorkerAgent != "table-driven" || with.WorkerPrompt != "Use table-driven tests." {
		t.Fatalf("override not round-tripped: %+v", with)
	}
	without := byID["T-WITHOUT"]
	if without.WorkerAgent != "" || without.WorkerPrompt != "" {
		t.Fatalf("unexpected override: %+v", without)
	}
}

func TestWorkerOverrideMigrationPreservesRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	legacy, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`
CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'available',
  priority INTEGER NOT NULL DEFAULT 0,
  scope_json TEXT NOT NULL DEFAULT '[]',
  source_ref TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO tasks (id, title, status, updated_at)
VALUES ('T-OLD', 'Old card', 'available', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ready, err := store.ListReadyTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].ID != "T-OLD" {
		t.Fatalf("migrated tasks = %+v", ready)
	}
	if ready[0].WorkerAgent != "" || ready[0].WorkerPrompt != "" {
		t.Fatalf("old row gained overrides: %+v", ready[0])
	}
}

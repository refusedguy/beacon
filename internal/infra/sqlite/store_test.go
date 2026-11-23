package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestRecursiveReadyTasks(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	tasks := []core.Task{
		{ID: "A", Status: core.TaskDone, Scope: []core.ScopePath{core.ScopeFile("a.go")}},
		{ID: "B", Status: core.TaskAvailable, DependsOn: []core.TaskID{"A"}, Scope: []core.ScopePath{core.ScopeFile("b.go")}},
		{ID: "C", Status: core.TaskAvailable, DependsOn: []core.TaskID{"B"}, Scope: []core.ScopePath{core.ScopeFile("c.go")}},
		{ID: "D", Status: core.TaskAvailable, DependsOn: []core.TaskID{"E"}, Scope: []core.ScopePath{core.ScopeFile("d.go")}},
		{ID: "E", Status: core.TaskAvailable, DependsOn: []core.TaskID{"D"}, Scope: []core.ScopePath{core.ScopeFile("e.go")}},
		{ID: "F", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("f.go")}},
	}
	if err := store.SyncTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	ready, err := store.ListReadyTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 2 || ready[0].ID != "B" || ready[1].ID != "F" {
		t.Fatalf("ready = %+v", ready)
	}

	tasks[1].Status = core.TaskDone
	if err := store.SyncTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	ready, err = store.ListReadyTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range ready {
		if r.ID == "C" {
			found = true
		}
		if r.ID == "D" || r.ID == "E" {
			t.Fatalf("cycle leaked into ready set: %s", r.ID)
		}
	}
	if !found {
		t.Fatalf("transitively unblocked task C is missing: %+v", ready)
	}
}

func TestSetTaskStatusUnblocksDependent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	tasks := []core.Task{
		{ID: "A", Status: core.TaskAvailable},
		{ID: "B", Status: core.TaskAvailable, DependsOn: []core.TaskID{"A"}},
	}
	if err := store.SyncTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	ready, err := store.ListReadyTasks(ctx)
	if err != nil || len(ready) != 1 || ready[0].ID != "A" {
		t.Fatalf("before: %+v %v", ready, err)
	}
	if err := store.SetTaskStatus(ctx, "A", "claimed"); err != nil {
		t.Fatal(err)
	}
	ready, err = store.ListReadyTasks(ctx)
	if err != nil || len(ready) != 0 {
		t.Fatalf("after claim: %+v %v", ready, err)
	}
	if err := store.SetTaskStatus(ctx, "A", "done"); err != nil {
		t.Fatal(err)
	}
	ready, err = store.ListReadyTasks(ctx)
	if err != nil || len(ready) != 1 || ready[0].ID != "B" {
		t.Fatalf("after done: %+v %v", ready, err)
	}
}

func TestRunScopeAndIncidentPersistence(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	run := core.NewRun("r1", "T-1", time.Now().UTC())
	run.Scope = []core.ScopePath{core.ScopeDir("internal/app"), core.ScopePattern("internal/**/*.go")}
	run.ParentSessionID = "sess-parent"
	run.IsFixer = true
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "r1")
	if err != nil || got == nil {
		t.Fatalf("get = %+v err=%v", got, err)
	}
	if len(got.Scope) != 2 || got.ParentSessionID != "sess-parent" || !got.IsFixer {
		t.Fatalf("bad roundtrip: %+v", got)
	}
	in, err := store.CreateIncident(ctx, &core.BaseIncident{
		Repo: "o/r", BaseBranch: "main", Signature: "sig", FailedChecksJSON: "[]",
	})
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.CreateIncident(ctx, &core.BaseIncident{
		Repo: "o/r", BaseBranch: "main", Signature: "sig", FailedChecksJSON: "[]",
	})
	if err != nil || again.ID != in.ID {
		t.Fatalf("incident not idempotent: %+v %v", again, err)
	}
}

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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

func TestSyncTasksPersistsEpicGraph(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	plan := []core.ChildSpec{{
		Title: "Child one", Scope: []core.ScopePath{core.ScopeFile("a.go")},
		Depends: []core.TaskID{"EXT-1"}, Priority: 7,
	}}
	tasks := []core.Task{
		{ID: "T-EPIC", Title: "Epic", Status: core.TaskAvailable,
			Kind: core.TaskKindEpic, BaseBranch: "main",
			RequireReview: core.ReviewModeHuman, Plan: plan},
		{ID: "T-EPIC-01", Title: "Child one", Status: core.TaskAvailable,
			Kind: core.TaskKindTask, BaseBranch: "beacon/T-EPIC",
			DependsOn: []core.TaskID{"EXT-1"}, Priority: 7,
			Scope: []core.ScopePath{core.ScopeFile("a.go")}},
	}
	if err := store.SyncTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}

	var kind, base, review, planJSON string
	if err := store.db.QueryRowContext(ctx,
		`SELECT kind, base_branch, require_review, plan_json FROM tasks WHERE id='T-EPIC'`,
	).Scan(&kind, &base, &review, &planJSON); err != nil {
		t.Fatal(err)
	}
	if kind != "epic" || base != "main" || review != "human" {
		t.Fatalf("epic fields = %q %q %q", kind, base, review)
	}
	var gotPlan []core.ChildSpec
	if err := json.Unmarshal([]byte(planJSON), &gotPlan); err != nil {
		t.Fatal(err)
	}
	if len(gotPlan) != 1 || gotPlan[0].Title != "Child one" || len(gotPlan[0].Depends) != 1 {
		t.Fatalf("plan = %+v", gotPlan)
	}

	var childBase string
	if err := store.db.QueryRowContext(ctx,
		`SELECT base_branch FROM tasks WHERE id='T-EPIC-01'`,
	).Scan(&childBase); err != nil {
		t.Fatal(err)
	}
	if childBase != "beacon/T-EPIC" {
		t.Fatalf("child base = %q", childBase)
	}

	ready, err := store.ListReadyTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, task := range ready {
		if task.ID != "T-EPIC-01" {
			continue
		}
		found = true
		if task.BaseBranch != "beacon/T-EPIC" || task.Kind != core.TaskKindTask {
			t.Fatalf("ready child = %+v", task)
		}
	}
	if !found {
		t.Fatalf("epic child is not ready: %+v", ready)
	}
}

func TestListRecent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	recent, err := store.ListRecent(ctx, 10)
	if err != nil || len(recent) != 0 {
		t.Fatalf("empty result = %+v err=%v", recent, err)
	}

	type entry struct {
		id    core.RunID
		state core.RunState
	}
	entries := []entry{
		{"r0", core.RunMerged},
		{"r1", core.RunRunning},
		{"r2", core.RunState("dropped")},
		{"r3", core.RunClosed},
	}
	for _, e := range entries {
		r := core.NewRun(e.id, "T-1", time.Now().UTC())
		r.State = e.state
		if err := store.Save(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	recent, err = store.ListRecent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []core.RunID{"r3", "r2", "r0"}
	if len(recent) != len(want) {
		t.Fatalf("len = %d, want %d: %+v", len(recent), len(want), recent)
	}
	for i, id := range want {
		if recent[i].ID != id {
			t.Fatalf("recent[%d] = %s, want %s", i, recent[i].ID, id)
		}
	}

	recent, err = store.ListRecent(ctx, 2)
	if err != nil || len(recent) != 2 {
		t.Fatalf("limited result = %+v err=%v", recent, err)
	}
	if recent[0].ID != "r3" || recent[1].ID != "r2" {
		t.Fatalf("limited order = %+v", recent)
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

func TestObservedBaseSHARoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	run := core.NewRun("r1", "T-1", time.Now().UTC())
	run.ObservedSHA = "head-1"
	run.ObservedBaseSHA = "base-1"
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "r1")
	if err != nil || got == nil {
		t.Fatalf("get = %+v err=%v", got, err)
	}
	if got.ObservedSHA != "head-1" || got.ObservedBaseSHA != "base-1" {
		t.Fatalf("bad observed roundtrip: %+v", got)
	}

	run.ObservedBaseSHA = "base-2"
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx, "r1")
	if err != nil || got == nil {
		t.Fatalf("get after update = %+v err=%v", got, err)
	}
	if got.ObservedBaseSHA != "base-2" {
		t.Fatalf("ObservedBaseSHA = %q, want base-2", got.ObservedBaseSHA)
	}
}

func TestObservedBaseSHAMigrationPreservesRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	legacy, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL,
  state TEXT NOT NULL,
  pr_number INTEGER NOT NULL DEFAULT 0,
  head_sha TEXT NOT NULL DEFAULT '',
  observed_sha TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  parent_session_id TEXT NOT NULL DEFAULT '',
  workspace_path TEXT NOT NULL DEFAULT '',
  branch TEXT NOT NULL DEFAULT '',
  scope_json TEXT NOT NULL DEFAULT '[]',
  blocked_incident_id TEXT NOT NULL DEFAULT '',
  is_fixer INTEGER NOT NULL DEFAULT 0,
  rebase_attempts INTEGER NOT NULL DEFAULT 0,
  fix_attempts INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 3,
  reason TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO runs (id, task_id, state, observed_sha, created_at, updated_at)
VALUES ('r1', 'T-1', 'waiting_ci', 'head-1', 1, 2)`); err != nil {
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
	got, err := store.Get(context.Background(), "r1")
	if err != nil || got == nil {
		t.Fatalf("get migrated run = %+v err=%v", got, err)
	}
	if got.ObservedSHA != "head-1" || got.ObservedBaseSHA != "" {
		t.Fatalf("bad migrated run: %+v", got)
	}
	got.ObservedBaseSHA = "base-1"
	if err := store.Save(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(context.Background(), "r1")
	if err != nil || got == nil || got.ObservedBaseSHA != "base-1" {
		t.Fatalf("write after migration = %+v err=%v", got, err)
	}
}

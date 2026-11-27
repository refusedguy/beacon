package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

func TestRecursiveReadyTasks(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
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
		t.Fatalf("store.SyncTasks(ctx, tasks): %v", err)
	}
	ready, err := store.ListReadyTasks(ctx)
	if err != nil {
		t.Fatalf("store.ListReadyTasks(ctx): %v", err)
	}
	if len(ready) != 2 || ready[0].ID != "B" || ready[1].ID != "F" {
		t.Fatalf("ready = %+v", ready)
	}

	tasks[1].Status = core.TaskDone
	if err := store.SyncTasks(ctx, tasks); err != nil {
		t.Fatalf("store.SyncTasks(ctx, tasks): %v", err)
	}
	ready, err = store.ListReadyTasks(ctx)
	if err != nil {
		t.Fatalf("store.ListReadyTasks(ctx): %v", err)
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
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	tasks := []core.Task{
		{ID: "A", Status: core.TaskAvailable},
		{ID: "B", Status: core.TaskAvailable, DependsOn: []core.TaskID{"A"}},
	}
	if err := store.SyncTasks(ctx, tasks); err != nil {
		t.Fatalf("store.SyncTasks(ctx, tasks): %v", err)
	}
	ready, err := store.ListReadyTasks(ctx)
	if err != nil || len(ready) != 1 || ready[0].ID != "A" {
		t.Fatalf("before: %+v %v", ready, err)
	}
	if err := store.SetTaskStatus(ctx, "A", "claimed"); err != nil {
		t.Fatalf("store.SetTaskStatus(ctx, \"A\", \"claimed\"): %v", err)
	}
	ready, err = store.ListReadyTasks(ctx)
	if err != nil || len(ready) != 0 {
		t.Fatalf("after claim: %+v %v", ready, err)
	}
	if err := store.SetTaskStatus(ctx, "A", "done"); err != nil {
		t.Fatalf("store.SetTaskStatus(ctx, \"A\", \"done\"): %v", err)
	}
	ready, err = store.ListReadyTasks(ctx)
	if err != nil || len(ready) != 1 || ready[0].ID != "B" {
		t.Fatalf("after done: %+v %v", ready, err)
	}
}

func TestSyncTasksPersistsEpicGraph(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
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
		t.Fatalf("store.SyncTasks(ctx, tasks): %v", err)
	}

	var kind, base, review, planJSON string
	if err := store.db.QueryRowContext(ctx,
		`SELECT kind, base_branch, require_review, plan_json FROM tasks WHERE id='T-EPIC'`,
	).Scan(&kind, &base, &review, &planJSON); err != nil {
		t.Fatalf("store.db.QueryRowContext(SELECT kind, base_branch, require_review, plan_json FROM tasks WHERE id='T-EPIC').Scan: %v", err)
	}
	if kind != "epic" || base != "main" || review != "human" {
		t.Fatalf("epic fields = %q %q %q", kind, base, review)
	}
	var gotPlan []core.ChildSpec
	if err := json.Unmarshal([]byte(planJSON), &gotPlan); err != nil {
		t.Fatalf("json.Unmarshal([]byte(planJSON), &gotPlan): %v", err)
	}
	if len(gotPlan) != 1 || gotPlan[0].Title != "Child one" || len(gotPlan[0].Depends) != 1 {
		t.Fatalf("plan = %+v", gotPlan)
	}

	var childBase string
	if err := store.db.QueryRowContext(ctx,
		`SELECT base_branch FROM tasks WHERE id='T-EPIC-01'`,
	).Scan(&childBase); err != nil {
		t.Fatalf("store.db.QueryRowContext(SELECT base_branch FROM tasks WHERE id='T-EPIC-01').Scan: %v", err)
	}
	if childBase != "beacon/T-EPIC" {
		t.Fatalf("child base = %q", childBase)
	}

	ready, err := store.ListReadyTasks(ctx)
	if err != nil {
		t.Fatalf("store.ListReadyTasks(ctx): %v", err)
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
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
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
			t.Fatalf("store.Save(ctx, r): %v", err)
		}
	}

	recent, err = store.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("store.ListRecent(ctx, 10): %v", err)
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
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	run := core.NewRun("r1", "T-1", time.Now().UTC())
	run.Scope = []core.ScopePath{core.ScopeDir("internal/app"), core.ScopePattern("internal/**/*.go")}
	run.ParentSessionID = "sess-parent"
	run.IsFixer = true
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf("store.Save(ctx, run): %v", err)
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
		t.Fatalf("store.CreateIncident(Repo: \"o/r\", BaseBranch: \"main\"): %v", err)
	}
	again, err := store.CreateIncident(ctx, &core.BaseIncident{
		Repo: "o/r", BaseBranch: "main", Signature: "sig", FailedChecksJSON: "[]",
	})
	if err != nil || again.ID != in.ID {
		t.Fatalf("incident not idempotent: %+v %v", again, err)
	}
}

func TestCreateIncidentReopensResolvedSignature(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	in, err := store.CreateIncident(ctx, &core.BaseIncident{
		Repo: "o/r", BaseBranch: "main", Signature: "sig-a",
		FailedChecksJSON: `["check"]`, Attempts: 2,
	})
	if err != nil || in == nil {
		t.Fatalf("create = %+v err=%v", in, err)
	}
	in.Status = "resolved"
	in.Attempts = 2
	if err := store.UpdateIncident(ctx, in); err != nil {
		t.Fatalf("store.UpdateIncident(ctx, in): %v", err)
	}
	if open, err := store.FindOpenIncident(ctx, "o/r", "main", "sig-a"); err != nil || open != nil {
		t.Fatalf("resolved incident still open: %+v err=%v", open, err)
	}

	reopened, err := store.CreateIncident(ctx, &core.BaseIncident{
		Repo: "o/r", BaseBranch: "main", Signature: "sig-a",
		FailedChecksJSON: `["check","other"]`,
	})
	if err != nil || reopened == nil {
		t.Fatalf("reopen = %+v err=%v (must not be nil, nil)", reopened, err)
	}
	if reopened.Status != "open" || reopened.Attempts != 0 || reopened.FixerRunID != "" {
		t.Fatalf("reopened incident = %+v", reopened)
	}
	if reopened.FailedChecksJSON != `["check","other"]` {
		t.Fatalf("failed checks = %s", reopened.FailedChecksJSON)
	}
}

func TestCurrentIncidentNewestOpenByCreatedAtThenID(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	current, err := store.CurrentIncident(ctx)
	if err != nil || current != nil {
		t.Fatalf("empty current = %+v err=%v", current, err)
	}

	insert := func(id, status string, createdAt int64) {
		t.Helper()
		_, err := store.db.ExecContext(ctx, `
INSERT INTO base_incidents(id, repo, base_branch, signature, failed_checks_json, status, created_at)
VALUES (?,?,?,?,?,?,?)`, id, "o/r", "main", "sig-"+id, "[]", status, createdAt)
		if err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	insert("inc-a", "open", 10)
	insert("inc-b", "open", 20)
	insert("inc-c", "open", 20)
	insert("inc-d", "resolved", 30)

	current, err = store.CurrentIncident(ctx)
	if err != nil || current == nil {
		t.Fatalf("current = %+v err=%v", current, err)
	}
	if current.ID != "inc-c" {
		t.Fatalf("current id = %s, want inc-c", current.ID)
	}
}

func TestObservedBaseSHARoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	run := core.NewRun("r1", "T-1", time.Now().UTC())
	run.ObservedSHA = "head-1"
	run.ObservedBaseSHA = "base-1"
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf("store.Save(ctx, run): %v", err)
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
		t.Fatalf("store.Save(ctx, run): %v", err)
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
		t.Fatalf("sql.Open(\"sqlite\", \"file:\"+path): %v", err)
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
		t.Fatalf("legacy.Exec(CREATE TABLE runs legacy schema): %v", err)
	}
	if _, err := legacy.Exec(`INSERT INTO runs (id, task_id, state, observed_sha, created_at, updated_at)
VALUES ('r1', 'T-1', 'waiting_ci', 'head-1', 1, 2)`); err != nil {
		t.Fatalf("legacy.Exec(`INSERT INTO runs (id, task_id, state, observed_sha, created_at, updated_at): %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("legacy.Close(): %v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open(path): %v", err)
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
		t.Fatalf("store.Save(context.Background(), got): %v", err)
	}
	got, err = store.Get(context.Background(), "r1")
	if err != nil || got == nil || got.ObservedBaseSHA != "base-1" {
		t.Fatalf("write after migration = %+v err=%v", got, err)
	}
}

func TestRunEventsRecordStateTransitions(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	base := time.Now().UTC().Add(-time.Minute)
	run := core.NewRun("r1", "T-1", base)
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf("store.Save(ctx, run): %v", err)
	}
	for i, state := range []core.RunState{core.RunRunning, core.RunWaitingCI, core.RunBlocked} {
		run.State = state
		run.UpdatedAt = base.Add(time.Duration(i+1) * time.Second)
		if err := store.Save(ctx, run); err != nil {
			t.Fatalf("store.Save(ctx, run): %v", err)
		}
	}
	// Saving without a state change must not pad the timeline.
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf("store.Save(ctx, run): %v", err)
	}

	events, err := store.ListRunEvents(ctx, []string{"r1"})
	if err != nil {
		t.Fatalf("store.ListRunEvents(ctx, []string{\"r1\"}): %v", err)
	}
	want := []string{"pending", "running", "waiting_ci", "blocked"}
	if len(events) != len(want) {
		t.Fatalf("events = %d, want %d: %+v", len(events), len(want), events)
	}
	for i, state := range want {
		if events[i].State != state || events[i].At.IsZero() || events[i].RunID != "r1" {
			t.Errorf("event[%d] = %+v, want state %s", i, events[i], state)
		}
		if i > 0 && !events[i].At.After(events[i-1].At) {
			t.Errorf("event[%d] time %s is not after %s", i, events[i].At, events[i-1].At)
		}
	}
}

func TestSavePreservesDomainUpdatedAt(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	stamp := time.Date(2026, 11, 25, 10, 30, 0, 123456789, time.UTC)
	run := core.NewRun("r1", "T-1", stamp.Add(-time.Minute))
	run.UpdatedAt = stamp
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf("store.Save(ctx, run): %v", err)
	}
	if !run.UpdatedAt.Equal(stamp) {
		t.Fatalf("UpdatedAt after save = %s, want %s", run.UpdatedAt, stamp)
	}
	got, err := store.Get(ctx, "r1")
	if err != nil || got == nil {
		t.Fatalf("get = %+v err=%v", got, err)
	}
	if !got.UpdatedAt.Equal(stamp) {
		t.Fatalf("stored UpdatedAt = %s, want %s", got.UpdatedAt, stamp)
	}

	blank := core.NewRun("r2", "T-1", time.Time{})
	if err := store.Save(ctx, blank); err != nil {
		t.Fatalf("store.Save(ctx, blank): %v", err)
	}
	if blank.CreatedAt.IsZero() || blank.UpdatedAt.IsZero() {
		t.Fatalf("missing timestamps were not stamped: %+v", blank)
	}
}

func TestDecisionsAndSnapshotAreBatched(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	logger := app.DecisionLogger{Sink: store}

	first := core.NewRun("r1", "T-1", time.Now().UTC().Add(-2*time.Minute))
	first.State = core.RunWaitingCI
	second := core.NewRun("r2", "T-2", time.Now().UTC().Add(-time.Minute))
	second.State = core.RunMerged
	for _, run := range []*core.Run{first, second} {
		if err := store.Save(ctx, run); err != nil {
			t.Fatalf("store.Save(ctx, run): %v", err)
		}
	}
	logger.Record(ctx, app.Decision{
		Kind: "dispatch.skip", RunID: "r1", TaskID: "T-1",
		Verdict: "scope_overlap", Reason: "scope overlaps T-9",
		Fields: map[string]any{"blocked_by": "T-9"},
	})
	logger.Record(ctx, app.Decision{
		Kind: "transition.merge", RunID: "r2", PRNumber: 42, Action: "merged",
	})
	logger.Record(ctx, app.Decision{Kind: "dispatch.candidates"})

	decisions, err := store.ListDecisions(ctx, []string{"r1", "r2"}, 0, 0)
	if err != nil || len(decisions) != 3 {
		t.Fatalf("decisions = %+v err=%v", decisions, err)
	}
	if decisions[0].Verdict != "scope_overlap" || decisions[0].Fields["blocked_by"] != "T-9" || decisions[0].At.IsZero() {
		t.Fatalf("stored decision = %+v", decisions[0])
	}
	events, err := store.ListRunEvents(ctx, []string{"r1", "r2"})
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %+v err=%v", events, err)
	}

	snap, err := (app.SnapshotService{Store: store}).Snapshot(ctx)
	if err != nil {
		t.Fatalf("(app.SnapshotService{Store: store}).Snapshot(ctx): %v", err)
	}
	if len(snap.Active) != 1 || len(snap.Recent) != 1 {
		t.Fatalf("snapshot runs = %+v", snap)
	}
	active := snap.Active[0]
	if active.ID != "r1" || len(active.Timeline) != 1 || len(active.Decisions) != 1 {
		t.Fatalf("active run = %+v", active)
	}
	if active.Durations.WaitingCI < 60 {
		t.Fatalf("waiting_ci duration = %d, want at least 60s", active.Durations.WaitingCI)
	}
	m := snap.Metrics
	if m.SkipCount != 1 || m.Skips["scope_overlap"] != 1 || m.MergeCount != 1 ||
		m.DispatchCycles != 1 || m.ActiveCount != 1 || m.RecentCount != 1 || m.CIWaitSeconds < 60 {
		t.Fatalf("metrics = %+v", m)
	}
}

func TestListDecisionsCapsHistoryButKeepsMetricTotals(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	logger := app.DecisionLogger{Sink: store}

	run := core.NewRun("r1", "T-1", time.Now().UTC())
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf("store.Save(ctx, run): %v", err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		logger.Record(ctx, app.Decision{
			Kind: "dispatch.skip", RunID: "r1", Verdict: "overlap",
			At: base.Add(time.Duration(i) * time.Second),
		})
	}
	for i := 0; i < 7; i++ {
		logger.Record(ctx, app.Decision{
			Kind: "dispatch.candidates", At: base.Add(time.Duration(i) * time.Second),
		})
	}

	decisions, err := store.ListDecisions(ctx, []string{"r1"}, 2, 3)
	if err != nil {
		t.Fatalf("store.ListDecisions(ctx, []string{\"r1\"}, 2, 3): %v", err)
	}
	if len(decisions) != 5 {
		t.Fatalf("decisions = %d, want 2 per-run + 3 orphan", len(decisions))
	}
	inRun, orphan := 0, 0
	for _, d := range decisions {
		if d.RunID == "r1" {
			inRun++
		} else {
			orphan++
		}
	}
	if inRun != 2 || orphan != 3 {
		t.Fatalf("in-run=%d orphan=%d", inRun, orphan)
	}

	counts, err := store.DecisionCounts(ctx, []string{"r1"})
	if err != nil {
		t.Fatalf("store.DecisionCounts(ctx, []string{\"r1\"}): %v", err)
	}
	totalSkips, totalCycles := 0, 0
	for _, c := range counts {
		switch c.Kind {
		case "dispatch.skip":
			totalSkips += c.Count
		case "dispatch.candidates":
			totalCycles += c.Count
		}
	}
	if totalSkips != 5 || totalCycles != 7 {
		t.Fatalf("counts: skips=%d cycles=%d", totalSkips, totalCycles)
	}

	snap, err := (app.SnapshotService{Store: store, DecisionsLimit: 2, OrphanDecisionsLimit: 3}).Snapshot(ctx)
	if err != nil {
		t.Fatalf("(app.SnapshotService{Store: store, DecisionsLimit: 2, OrphanDecisionsLimit: 3}).Snapshot(ctx): %v", err)
	}
	if snap.Metrics.SkipCount != 5 || snap.Metrics.DispatchCycles != 7 {
		t.Fatalf("snapshot metrics = %+v", snap.Metrics)
	}
	if len(snap.Active[0].Decisions) != 2 {
		t.Fatalf("snapshot decisions = %d, want 2", len(snap.Active[0].Decisions))
	}
}

func TestSnapshotIncludesLocalQueueAndBaseIncident(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	tasks := []core.Task{
		{ID: "T-1", Title: "ready card", Priority: 5, Status: core.TaskAvailable},
		{ID: "T-2", Title: "blocked card", Priority: 3, Status: core.TaskAvailable, DependsOn: []core.TaskID{"T-1"}},
		{ID: "T-3", Title: "done card", Priority: 9, Status: core.TaskDone},
	}
	if err := store.SyncTasks(ctx, tasks); err != nil {
		t.Fatalf("store.SyncTasks(ctx, tasks): %v", err)
	}
	incident, err := store.CreateIncident(ctx, &core.BaseIncident{
		Repo: "owner/repo", BaseBranch: "main", Signature: "sig-1",
		FailedChecksJSON: `[{"name":"build"}]`, FixerRunID: "r-fixer", Attempts: 2,
	})
	if err != nil || incident == nil {
		t.Fatalf("incident = %+v err=%v", incident, err)
	}
	run := core.NewRun("r-snap", "T-OUT", time.Now().UTC())
	run.State = core.RunWaitingCI
	run.PRNumber = 42
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf("store.Save(ctx, run): %v", err)
	}

	snap, err := (app.SnapshotService{Store: store, Repo: "owner/repo"}).Snapshot(ctx)
	if err != nil {
		t.Fatalf("(app.SnapshotService{Store: store, Repo: \"owner/repo\"}).Snapshot(ctx): %v", err)
	}
	if len(snap.Queue) != 2 {
		t.Fatalf("queue = %+v, want two open cards", snap.Queue)
	}
	if snap.Queue[0].ID != "T-1" || snap.Queue[0].Status != "ready" || snap.Queue[0].DependsOn == nil {
		t.Fatalf("first queue item = %+v", snap.Queue[0])
	}
	if snap.Queue[1].ID != "T-2" || snap.Queue[1].Status != "waiting_deps" ||
		len(snap.Queue[1].DependsOn) != 1 || snap.Queue[1].DependsOn[0] != "T-1" {
		t.Fatalf("second queue item = %+v", snap.Queue[1])
	}
	if snap.BaseIncident == nil || snap.BaseIncident.ID != incident.ID ||
		snap.BaseIncident.BaseBranch != "main" || len(snap.BaseIncident.FailedChecks) != 1 ||
		snap.BaseIncident.FailedChecks[0] != "build" || snap.BaseIncident.Attempts != 2 {
		t.Fatalf("base incident = %+v", snap.BaseIncident)
	}
	if snap.Daemon == nil || snap.Daemon.State == "" {
		t.Fatalf("daemon = %+v, want non-null", snap.Daemon)
	}
	if len(snap.Active) != 1 || snap.Active[0].PRURL != "https://github.com/owner/repo/pull/42" {
		t.Fatalf("active = %+v", snap.Active)
	}
}

func TestLastDecisionReturnsNewestForRun(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	if last, err := store.LastDecision(ctx, "r-none"); err != nil || last != nil {
		t.Fatalf("missing decision = %+v err=%v", last, err)
	}
	base := time.Now().UTC().Add(-time.Minute)
	for i, tc := range []struct{ reason, sha string }{{"first", "sha0"}, {"second", "sha1"}} {
		if err := store.RecordDecision(ctx, app.Decision{
			Kind: "reconcile.decision", RunID: "r1", Verdict: "wait",
			Reason: tc.reason, At: base.Add(time.Duration(i) * time.Second),
			Fields: map[string]any{"head_sha": tc.sha, "state": "waiting_ci"},
		}); err != nil {
			t.Fatalf("store.RecordDecision(RunID: \"r1\"): %v", err)
		}
	}
	last, err := store.LastDecision(ctx, "r1")
	if err != nil {
		t.Fatalf("store.LastDecision(ctx, \"r1\"): %v", err)
	}
	if last == nil || last.Reason != "second" || last.Fields["head_sha"] != "sha1" {
		t.Fatalf("last decision = %+v", last)
	}
}

// TestDecisionReadersDecodeIdentically checks that ListDecisions and
// LastDecision share one row decoder. The same stored decision must come back
// byte-identical from both readers, including its timestamp and fields.
func TestDecisionReadersDecodeIdentically(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	want := app.Decision{
		ID: 1, At: at, CycleID: "cycle-1", Kind: "reconcile.decision",
		RunID: "r1", TaskID: "T-1", PRNumber: 42, Verdict: "wait",
		Action: "retry", Reason: "waiting on CI",
		Fields: map[string]any{"head_sha": "sha1", "state": "waiting_ci", "n": float64(3)},
	}
	if err := store.RecordDecision(ctx, want); err != nil {
		t.Fatalf("store.RecordDecision: %v", err)
	}

	listed, err := store.ListDecisions(ctx, []string{"r1"}, 0, 0)
	if err != nil {
		t.Fatalf("store.ListDecisions: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("decisions = %d, want 1", len(listed))
	}
	last, err := store.LastDecision(ctx, "r1")
	if err != nil {
		t.Fatalf("store.LastDecision: %v", err)
	}
	if last == nil {
		t.Fatal("LastDecision returned nil, want one decision")
	}
	if !reflect.DeepEqual(listed[0], *last) {
		t.Fatalf("decoded decisions differ:\nListDecisions: %+v\nLastDecision: %+v", listed[0], *last)
	}
	if !listed[0].At.Equal(at) || listed[0].Fields["head_sha"] != "sha1" {
		t.Fatalf("decoded decision lost data: %+v", listed[0])
	}
}

func TestRunEventMigrationBackfillsExistingRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	legacy, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open(\"sqlite\", \"file:\"+path): %v", err)
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
);
INSERT INTO runs (id, task_id, state, created_at, updated_at) VALUES ('r1', 'T-1', 'waiting_ci', 1, 2);`)
	if err != nil {
		t.Fatalf("legacy.Exec(INSERT INTO runs ('r1', 'T-1', 'waiting_ci')): %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("legacy.Close(): %v", err)
	}

	for i := 0; i < 2; i++ {
		store, err := Open(path)
		if err != nil {
			t.Fatalf("Open(path): %v", err)
		}
		events, err := store.ListRunEvents(context.Background(), []string{"r1"})
		if err != nil {
			t.Fatalf("store.ListRunEvents(context.Background(), []string{\"r1\"}): %v", err)
		}
		if len(events) != 2 || events[0].State != "pending" || events[1].State != "waiting_ci" {
			t.Fatalf("open %d: backfilled events = %+v", i, events)
		}
		run, err := store.Get(context.Background(), "r1")
		if err != nil || run == nil || run.State != core.RunWaitingCI {
			t.Fatalf("open %d: run = %+v err=%v", i, run, err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("store.Close(): %v", err)
		}
	}
}

func TestListQueueTasksFiltersByStatusAndActiveRuns(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	tasks := []core.Task{
		{ID: "T-READY", Title: "ready", Priority: 5, Status: core.TaskAvailable},
		{ID: "T-TODO", Title: "todo", Priority: 4, Status: core.TaskStatus("todo")},
		{ID: "T-CLAIMED", Title: "claimed", Priority: 8, Status: core.TaskClaimed},
		{ID: "T-DONE", Title: "done", Priority: 9, Status: core.TaskDone},
		{ID: "T-DROPPED", Title: "dropped", Priority: 7, Status: core.TaskDropped},
		{ID: "T-RUNNING", Title: "running", Priority: 6, Status: core.TaskAvailable},
		{ID: "T-BLOCKED", Title: "blocked", Priority: 6, Status: core.TaskAvailable},
		{ID: "T-CLOSED", Title: "closed", Priority: 6, Status: core.TaskAvailable},
		{ID: "T-MERGED", Title: "merged", Priority: 6, Status: core.TaskAvailable},
		{ID: "T-WAITING", Title: "waiting", Priority: 2, Status: core.TaskAvailable,
			DependsOn: []core.TaskID{"T-READY"}},
	}
	if err := store.SyncTasks(ctx, tasks); err != nil {
		t.Fatalf("store.SyncTasks(ctx, tasks): %v", err)
	}
	for _, tc := range []struct {
		task  string
		state core.RunState
	}{
		{"T-RUNNING", core.RunRunning},
		{"T-BLOCKED", core.RunBlocked},
		{"T-CLOSED", core.RunClosed},
		{"T-MERGED", core.RunMerged},
	} {
		run := core.NewRun(core.RunID("run-"+tc.task), core.TaskID(tc.task), time.Now().UTC())
		run.State = tc.state
		if err := store.Save(ctx, run); err != nil {
			t.Fatalf("store.Save(ctx, run): %v", err)
		}
	}

	queue, err := store.ListQueueTasks(ctx)
	if err != nil {
		t.Fatalf("store.ListQueueTasks(ctx): %v", err)
	}
	want := []string{"T-CLOSED", "T-MERGED", "T-READY", "T-TODO", "T-WAITING"}
	if len(queue) != len(want) {
		t.Fatalf("queue = %+v, want %v", queue, want)
	}
	for i, id := range want {
		if queue[i].ID != id {
			t.Fatalf("queue[%d] = %s, want %s", i, queue[i].ID, id)
		}
		if queue[i].DependsOn == nil {
			t.Fatalf("%s has null dependencies", id)
		}
	}
	last := queue[len(queue)-1]
	if !last.Waiting || len(last.DependsOn) != 1 || last.DependsOn[0] != "T-READY" {
		t.Fatalf("waiting task = %+v", last)
	}
}

func TestEmptyRunListsEncodeAsArrays(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	lists := map[string]func() ([]*core.Run, error){
		"active": func() ([]*core.Run, error) { return store.ListActive(ctx) },
		"recent": func() ([]*core.Run, error) { return store.ListRecent(ctx, 0) },
		"incident": func() ([]*core.Run, error) {
			return store.ListByIncident(ctx, "no-such-incident")
		},
	}
	for name, list := range lists {
		runs, err := list()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if runs == nil {
			t.Fatalf("%s list is nil, want empty slice", name)
		}
		b, err := json.Marshal(runs)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(b) != "[]" {
			t.Fatalf("%s JSON = %s, want []", name, b)
		}
	}
}

func TestEmptySnapshotJSONHasStableKeysAndArrays(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	snap, err := (app.SnapshotService{Store: store}).Snapshot(ctx)
	if err != nil {
		t.Fatalf("(app.SnapshotService{Store: store}).Snapshot(ctx): %v", err)
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal(snap): %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("json.Unmarshal(b, &raw): %v", err)
	}
	for _, key := range []string{"active", "recent", "queue", "daemon", "base_incident"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("snapshot JSON is missing %q: %s", key, b)
		}
	}
	for _, key := range []string{"active", "recent", "queue"} {
		if got := string(raw[key]); got != "[]" {
			t.Fatalf("%s JSON = %s, want []", key, got)
		}
	}
	if got := string(raw["daemon"]); !strings.Contains(got, `"state":"absent"`) {
		t.Fatalf("daemon JSON = %s, want state absent", got)
	}
	if got := string(raw["base_incident"]); !strings.Contains(got, `"id":""`) {
		t.Fatalf("base incident JSON = %s, want empty id", got)
	}
}

func TestChecksRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	start := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)

	run := core.NewRun("r-checks", "T-1", time.Now().UTC())
	run.Checks = []core.CheckResult{{
		Name: "ci", State: core.CheckSuccess,
		StartedAt: start, CompletedAt: start.Add(28 * time.Second),
	}}
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf("store.Save(ctx, run): %v", err)
	}
	got, err := store.Get(ctx, "r-checks")
	if err != nil || got == nil || len(got.Checks) != 1 {
		t.Fatalf("get = %+v err=%v", got, err)
	}
	check := got.Checks[0]
	if check.Name != "ci" || check.State != core.CheckSuccess || check.DurationSeconds() != 28 {
		t.Fatalf("check = %+v", check)
	}
	if !check.StartedAt.Equal(start) || !check.CompletedAt.Equal(start.Add(28*time.Second)) {
		t.Fatalf("timestamps = %s / %s", check.StartedAt, check.CompletedAt)
	}

	run.Checks = nil
	if err := store.Save(ctx, run); err != nil {
		t.Fatalf("store.Save(ctx, run): %v", err)
	}
	got, err = store.Get(ctx, "r-checks")
	if err != nil || got == nil || got.Checks == nil || len(got.Checks) != 0 {
		t.Fatalf("cleared checks = %+v err=%v", got, err)
	}
}

func TestChecksMigrationPreservesRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	legacy, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open(\"sqlite\", \"file:\"+path): %v", err)
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
);
INSERT INTO runs (id, task_id, state, created_at, updated_at) VALUES ('r-old', 'T-1', 'waiting_ci', 1, 2);`)
	if err != nil {
		t.Fatalf("legacy.Exec(INSERT INTO runs ('r-old', 'T-1', 'waiting_ci')): %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("legacy.Close(): %v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open(path): %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	got, err := store.Get(ctx, "r-old")
	if err != nil || got == nil || got.State != core.RunWaitingCI {
		t.Fatalf("get migrated run = %+v err=%v", got, err)
	}
	if got.Checks == nil || len(got.Checks) != 0 {
		t.Fatalf("old run checks = %+v, want empty array", got.Checks)
	}
	got.Checks = []core.CheckResult{{Name: "ci", State: core.CheckFailure}}
	if err := store.Save(ctx, got); err != nil {
		t.Fatalf("store.Save(ctx, got): %v", err)
	}
	got, err = store.Get(ctx, "r-old")
	if err != nil || got == nil || len(got.Checks) != 1 || got.Checks[0].State != core.CheckFailure {
		t.Fatalf("write after migration = %+v err=%v", got, err)
	}
}

func countStoreRows(t *testing.T, store *Store, query string) int {
	t.Helper()
	var n int
	if err := store.db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("store.db.QueryRow(query).Scan(&n): %v", err)
	}
	return n
}

func seedStaleDeliveryRows(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	keys := []string{"merge:42:old", "merge:43:fresh"}
	for _, key := range keys {
		ok, err := store.ClaimDelivery(ctx, key)
		if err != nil || !ok {
			t.Fatalf("claim %s = %v, %v", key, ok, err)
		}
	}
	for _, r := range []struct{ id, sha string }{{"r-old", "old-sha"}, {"r-fresh", "fresh-sha"}} {
		if err := store.SetApproval(ctx, core.RunID(r.id), r.sha); err != nil {
			t.Fatalf("store.SetApproval(ctx, core.RunID(r.id), r.sha): %v", err)
		}
	}
	old := nowUnix() - (31 * 24 * time.Hour).Nanoseconds()
	if _, err := store.db.ExecContext(ctx, `UPDATE deliveries SET created_at=? WHERE key='merge:42:old'`, old); err != nil {
		t.Fatalf("store.db.ExecContext(ctx, `UPDATE deliveries SET created_at=? WHERE key='merge:42:old'`, old): %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE approvals SET created_at=? WHERE run_id='r-old'`, old); err != nil {
		t.Fatalf("store.db.ExecContext(ctx, `UPDATE approvals SET created_at=? WHERE run_id='r-old'`, old): %v", err)
	}
}

func TestPruneStaleRemovesOldDeliveryAndApprovalRows(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	seedStaleDeliveryRows(t, store)
	ctx := context.Background()

	removed, err := store.PruneStale(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("store.PruneStale(ctx, 30*24*time.Hour): %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2 (one delivery, one approval)", removed)
	}
	if got := countStoreRows(t, store, `SELECT COUNT(*) FROM deliveries`); got != 1 {
		t.Fatalf("deliveries = %d, want 1", got)
	}
	if got := countStoreRows(t, store, `SELECT COUNT(*) FROM approvals`); got != 1 {
		t.Fatalf("approvals = %d, want 1", got)
	}

	approved, err := store.Approved(ctx, "r-old", "old-sha")
	if err != nil || approved {
		t.Fatalf("old approval = %v, %v; want false", approved, err)
	}
	approved, err = store.Approved(ctx, "r-fresh", "fresh-sha")
	if err != nil || !approved {
		t.Fatalf("fresh approval = %v, %v; want true", approved, err)
	}
	ok, err := store.ClaimDelivery(ctx, "merge:42:old")
	if err != nil || !ok {
		t.Fatalf("reclaim old key = %v, %v; want true", ok, err)
	}

	// A second pass is harmless: there is nothing left inside the window.
	removed, err = store.PruneStale(ctx, 30*24*time.Hour)
	if err != nil || removed != 0 {
		t.Fatalf("second prune = %d, %v; want 0, nil", removed, err)
	}
}

func TestListActiveDoesNotPruneStaleRows(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	seedStaleDeliveryRows(t, store)

	// Status, dispatch and MCP reads all go through ListActive. A read must
	// leave the idempotency tables untouched, even with stale rows present.
	for i := 0; i < 2; i++ {
		active, err := store.ListActive(context.Background())
		if err != nil {
			t.Fatalf("store.ListActive(context.Background()): %v", err)
		}
		if len(active) != 0 {
			t.Fatalf("active = %+v, want none", active)
		}
	}
	if got := countStoreRows(t, store, `SELECT COUNT(*) FROM deliveries`); got != 2 {
		t.Fatalf("deliveries after reads = %d, want 2 (no DELETE)", got)
	}
	if got := countStoreRows(t, store, `SELECT COUNT(*) FROM approvals`); got != 2 {
		t.Fatalf("approvals after reads = %d, want 2 (no DELETE)", got)
	}
}

func TestConcurrentSavesDoNotReturnBusy(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		id := core.RunID(fmt.Sprintf("r-%d", i))
		go func() {
			errs <- store.Save(ctx, core.NewRun(id, "T-1", time.Now()))
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent store.Save: %v", err)
		}
	}
	for i := 0; i < n; i++ {
		got, err := store.Get(ctx, core.RunID(fmt.Sprintf("r-%d", i)))
		if err != nil || got == nil {
			t.Fatalf("run %d = %+v err=%v", i, got, err)
		}
	}
}

// TestSnapshotReadsKeepExactRows guards the query rewrites that make the deep
// snapshot benchmark fast. The rewrites must return the same decisions, in the
// same order, and the same exact counts as the window-function and OR forms.
func TestSnapshotReadsKeepExactRows(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open(filepath.Join(t.TempDir(), \"state.db\")): %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	base := time.Now().UTC().Add(-time.Hour)
	type row struct {
		runID   string
		kind    string
		verdict string
		at      time.Time
	}
	rows := []row{
		{"r1", "k1", "v1", base.Add(5 * time.Second)},
		{"r1", "k2", "v2", base.Add(time.Second)},
		{"r1", "k1", "v1", base.Add(3 * time.Second)},
		{"", "k1", "v1", base.Add(4 * time.Second)},
		{"", "k2", "v2", base.Add(2 * time.Second)},
		{"", "k1", "v1", base.Add(6 * time.Second)},
		{"r2", "k3", "v3", base.Add(7 * time.Second)},
	}
	for i, r := range rows {
		if err := store.RecordDecision(ctx, app.Decision{
			At: r.at, Kind: r.kind, RunID: r.runID, Verdict: r.verdict, Action: "a1",
		}); err != nil {
			t.Fatalf("RecordDecision %d: %v", i, err)
		}
	}

	decisions, err := store.ListDecisions(ctx, []string{"r1"}, 2, 2)
	if err != nil {
		t.Fatalf("ListDecisions: %v", err)
	}
	gotIDs := make([]int64, 0, len(decisions))
	for _, d := range decisions {
		gotIDs = append(gotIDs, d.ID)
	}
	wantIDs := []int64{3, 4, 1, 6}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("decision ids = %v, want %v", gotIDs, wantIDs)
	}

	counts, err := store.DecisionCounts(ctx, []string{"r1"})
	if err != nil {
		t.Fatalf("DecisionCounts: %v", err)
	}
	wantCounts := []app.DecisionCount{
		{Kind: "k1", Verdict: "v1", Action: "a1", Count: 4},
		{Kind: "k2", Verdict: "v2", Action: "a1", Count: 2},
	}
	if !reflect.DeepEqual(counts, wantCounts) {
		t.Fatalf("counts = %+v, want %+v", counts, wantCounts)
	}

	orphanCounts, err := store.DecisionCounts(ctx, nil)
	if err != nil {
		t.Fatalf("DecisionCounts(nil): %v", err)
	}
	wantOrphans := []app.DecisionCount{
		{Kind: "k1", Verdict: "v1", Action: "a1", Count: 2},
		{Kind: "k2", Verdict: "v2", Action: "a1", Count: 1},
	}
	if !reflect.DeepEqual(orphanCounts, wantOrphans) {
		t.Fatalf("orphan counts = %+v, want %+v", orphanCounts, wantOrphans)
	}
}

// TestDecisionSummaryBackfillOnOpen checks that a state database written
// before the summary table existed gains correct totals on the next open.
func TestDecisionSummaryBackfillOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if err := store.RecordDecision(ctx, app.Decision{
			Kind: "reconcile", RunID: "r1", Verdict: "keep_working", Action: "retry",
		}); err != nil {
			t.Fatalf("RecordDecision: %v", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(`DROP TABLE decision_summary`); err != nil {
		t.Fatalf("DROP TABLE decision_summary: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw.Close: %v", err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer store.Close()
	counts, err := store.DecisionCounts(ctx, []string{"r1"})
	if err != nil {
		t.Fatalf("DecisionCounts: %v", err)
	}
	want := []app.DecisionCount{{Kind: "reconcile", Verdict: "keep_working", Action: "retry", Count: 4}}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("counts after backfill = %+v, want %+v", counts, want)
	}
}

// TestActiveTaskIDsProjectsOnlyTaskAndState checks the narrow read against the
// same nonterminal rule FindActiveByTask uses. A row with unreadable JSON proves
// the projection does not decode full runs.
func TestActiveTaskIDsProjectsOnlyTaskAndState(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	states := map[string]core.RunState{
		"r1": core.RunRunning,
		"r2": core.RunBlocked,
		"r3": core.RunClosed,
		"r4": core.RunState("dropped"),
		"r5": core.RunWaitingCI,
		"r6": core.RunMerged,
	}
	tasks := map[string]core.TaskID{
		"r1": "T-1", "r2": "T-2", "r3": "T-3", "r4": "T-4", "r5": "T-5", "r6": "T-6",
	}
	for id, state := range states {
		run := core.NewRun(core.RunID(id), tasks[id], time.Now().UTC())
		run.State = state
		if err := store.Save(ctx, run); err != nil {
			t.Fatalf("Save(%s): %v", id, err)
		}
	}
	if _, err := store.db.Exec(`UPDATE runs SET scope_json='{', checks_json='{' WHERE id='r5'`); err != nil {
		t.Fatalf("corrupt JSON columns: %v", err)
	}

	got, err := store.ActiveTaskIDs(ctx, []string{"T-1", "T-2", "T-3", "T-4", "T-5", "T-6", "T-none"})
	if err != nil {
		t.Fatalf("ActiveTaskIDs: %v", err)
	}
	want := map[string]core.RunState{
		"T-1": core.RunRunning,
		"T-2": core.RunBlocked,
		"T-5": core.RunWaitingCI,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ActiveTaskIDs = %+v, want %+v", got, want)
	}
	if _, err := store.FindActiveByTask(ctx, "T-5"); err == nil {
		t.Fatal("FindActiveByTask decoded the unreadable JSON, so the narrow read did not stay narrow")
	}

	empty, err := store.ActiveTaskIDs(ctx, nil)
	if err != nil {
		t.Fatalf("ActiveTaskIDs(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("ActiveTaskIDs(nil) = %+v, want empty", empty)
	}
}

// TestCountedReadsKeepExactRows pins the count passes that size the result
// slices for ListRunEvents and ListDecisions. A wrong count would show up as a
// wrong length, a missing row or an out-of-order timeline.
func TestCountedReadsKeepExactRows(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	const perRun = 150
	base := time.Now().UTC().Add(-time.Hour)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	for _, runID := range []string{"r1", "r2"} {
		for i := 0; i < perRun; i++ {
			at := base.Add(time.Duration(i) * time.Second).UnixNano()
			if _, err := tx.ExecContext(ctx, `INSERT INTO run_events (run_id, kind, state, at) VALUES (?,?,?,?)`, runID, "state", "running", at); err != nil {
				_ = tx.Rollback()
				t.Fatalf("insert event: %v", err)
			}
		}
	}
	for i := 0; i < perRun; i++ {
		at := base.Add(time.Duration(i) * time.Second).UnixNano()
		if _, err := tx.ExecContext(ctx, `INSERT INTO decisions (at, kind, run_id, fields_json) VALUES (?,?,?,?)`, at, "transition.spawn", "r1", "{}"); err != nil {
			_ = tx.Rollback()
			t.Fatalf("insert decision: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	events, err := store.ListRunEvents(ctx, []string{"r1", "r2"})
	if err != nil {
		t.Fatalf("ListRunEvents: %v", err)
	}
	if len(events) != 2*perRun {
		t.Fatalf("events = %d, want %d", len(events), 2*perRun)
	}
	for i := 1; i < len(events); i++ {
		if events[i].At.Before(events[i-1].At) {
			t.Fatalf("event[%d] at %s precedes event[%d] at %s", i, events[i].At, i-1, events[i-1].At)
		}
	}

	decisions, err := store.ListDecisions(ctx, []string{"r1"}, 10, 10)
	if err != nil {
		t.Fatalf("ListDecisions: %v", err)
	}
	if len(decisions) != 10 {
		t.Fatalf("decisions = %d, want 10", len(decisions))
	}
}

func TestInQueryExpandsListsInOrder(t *testing.T) {
	query, args := inQuery(`a IN (%s) AND b IN (%s)`, []string{"x", "y"}, []string{"z"})
	if want := `a IN (?,?) AND b IN (?)`; query != want {
		t.Fatalf("query = %q, want %q", query, want)
	}
	if want := []any{"x", "y", "z"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %+v, want %+v", args, want)
	}

	query, args = inQuery(`a IN (%s)`, nil)
	if want := `a IN ()`; query != want || len(args) != 0 {
		t.Fatalf("empty list: query = %q args = %+v, want %q and no args", query, args, want)
	}
}

func TestDecisionCountQueryBindsEveryPlaceholder(t *testing.T) {
	for _, tc := range []struct {
		runIDs []string
		limit  int
		orphan int
	}{
		{nil, 20, 100},
		{[]string{"r1"}, 20, 100},
		{[]string{"r1", "r2"}, 2, 3},
	} {
		query, args := decisionCountQuery(tc.runIDs, tc.limit, tc.orphan)
		if n := strings.Count(query, "?"); n != len(args) {
			t.Fatalf("runIDs=%v: %d placeholders but %d args: %q", tc.runIDs, n, len(args), query)
		}
	}
}

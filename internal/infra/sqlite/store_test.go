package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app"
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

func TestCreateIncidentReopensResolvedSignature(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
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

func TestRunEventsRecordStateTransitions(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	run := core.NewRun("r1", "T-1", time.Now().UTC().Add(-time.Minute))
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	for _, state := range []core.RunState{core.RunRunning, core.RunWaitingCI, core.RunBlocked} {
		run.State = state
		if err := store.Save(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	// Saving without a state change must not pad the timeline.
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}

	events, err := store.ListRunEvents(ctx, []string{"r1"})
	if err != nil {
		t.Fatal(err)
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

func TestDecisionsAndSnapshotAreBatched(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	app.SetDecisionSink(store)
	t.Cleanup(func() { app.SetDecisionSink(nil) })

	first := core.NewRun("r1", "T-1", time.Now().UTC().Add(-2*time.Minute))
	first.State = core.RunWaitingCI
	second := core.NewRun("r2", "T-2", time.Now().UTC().Add(-time.Minute))
	second.State = core.RunMerged
	for _, run := range []*core.Run{first, second} {
		if err := store.Save(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	app.RecordDecision(ctx, app.Decision{
		Kind: "dispatch.skip", RunID: "r1", TaskID: "T-1",
		Verdict: "scope_overlap", Reason: "scope overlaps T-9",
		Fields: map[string]any{"blocked_by": "T-9"},
	})
	app.RecordDecision(ctx, app.Decision{
		Kind: "transition.merge", RunID: "r2", PRNumber: 42, Action: "merged",
	})
	app.RecordDecision(ctx, app.Decision{Kind: "dispatch.candidates"})

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
		t.Fatal(err)
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
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	app.SetDecisionSink(store)
	t.Cleanup(func() { app.SetDecisionSink(nil) })

	run := core.NewRun("r1", "T-1", time.Now().UTC())
	if err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		app.RecordDecision(ctx, app.Decision{
			Kind: "dispatch.skip", RunID: "r1", Verdict: "overlap",
			At: base.Add(time.Duration(i) * time.Second),
		})
	}
	for i := 0; i < 7; i++ {
		app.RecordDecision(ctx, app.Decision{
			Kind: "dispatch.candidates", At: base.Add(time.Duration(i) * time.Second),
		})
	}

	decisions, err := store.ListDecisions(ctx, []string{"r1"}, 2, 3)
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
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
		t.Fatal(err)
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
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	tasks := []core.Task{
		{ID: "T-1", Title: "ready card", Priority: 5, Status: core.TaskAvailable},
		{ID: "T-2", Title: "blocked card", Priority: 3, Status: core.TaskAvailable, DependsOn: []core.TaskID{"T-1"}},
		{ID: "T-3", Title: "done card", Priority: 9, Status: core.TaskDone},
	}
	if err := store.SyncTasks(ctx, tasks); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}

	snap, err := (app.SnapshotService{Store: store, Repo: "owner/repo"}).Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
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
			t.Fatal(err)
		}
	}
	last, err := store.LastDecision(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if last == nil || last.Reason != "second" || last.Fields["head_sha"] != "sha1" {
		t.Fatalf("last decision = %+v", last)
	}
}

func TestRunEventMigrationBackfillsExistingRuns(t *testing.T) {
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
);
INSERT INTO runs (id, task_id, state, created_at, updated_at) VALUES ('r1', 'T-1', 'waiting_ci', 1, 2);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		events, err := store.ListRunEvents(context.Background(), []string{"r1"})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 2 || events[0].State != "pending" || events[1].State != "waiting_ci" {
			t.Fatalf("open %d: backfilled events = %+v", i, events)
		}
		run, err := store.Get(context.Background(), "r1")
		if err != nil || run == nil || run.State != core.RunWaitingCI {
			t.Fatalf("open %d: run = %+v err=%v", i, run, err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListQueueTasksFiltersByStatusAndActiveRuns(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
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
			t.Fatal(err)
		}
	}

	queue, err := store.ListQueueTasks(ctx)
	if err != nil {
		t.Fatal(err)
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

func TestEmptySnapshotJSONHasStableKeysAndArrays(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	snap, err := (app.SnapshotService{Store: store}).Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
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
		t.Fatal(err)
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
		t.Fatal(err)
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
);
INSERT INTO runs (id, task_id, state, created_at, updated_at) VALUES ('r-old', 'T-1', 'waiting_ci', 1, 2);`)
	if err != nil {
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
		t.Fatal(err)
	}
	got, err = store.Get(ctx, "r-old")
	if err != nil || got == nil || len(got.Checks) != 1 || got.Checks[0].State != core.CheckFailure {
		t.Fatalf("write after migration = %+v err=%v", got, err)
	}
}

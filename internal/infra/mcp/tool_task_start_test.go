package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/planner"
	"github.com/refusedguy/beacon/pkg/core"
)

// startSource records marks so a refusal can prove it touched no card.
type startSource struct {
	tasks  []core.Task
	marked []core.TaskID
}

func (s *startSource) ListAvailable(context.Context) ([]core.Task, error) {
	return append([]core.Task(nil), s.tasks...), nil
}

func (s *startSource) Complete(context.Context, core.TaskID) error { return nil }

func (s *startSource) MarkInWorkspace(_ context.Context, id core.TaskID, _, _ string) error {
	s.marked = append(s.marked, id)
	return nil
}

func (s *startSource) Create(context.Context, core.Task) (core.Task, error) {
	return core.Task{}, nil
}

// startStore records every run and status write so a refusal can prove it
// changed nothing outside the named task.
type startStore struct {
	active   []*core.Run
	saves    []core.RunID
	statuses []core.TaskID
	syncs    int
}

func (s *startStore) SyncTasks(context.Context, []core.Task) error {
	s.syncs++
	return nil
}

func (s *startStore) ListReadyTasks(context.Context) ([]core.Task, error) { return nil, nil }

func (s *startStore) ListActive(context.Context) ([]*core.Run, error) {
	return append([]*core.Run(nil), s.active...), nil
}

func (s *startStore) ListRecent(context.Context, int) ([]*core.Run, error) { return nil, nil }

func (s *startStore) Save(_ context.Context, run *core.Run) error {
	s.saves = append(s.saves, run.ID)
	return nil
}

func (s *startStore) SetTaskStatus(_ context.Context, id core.TaskID, _ string) error {
	s.statuses = append(s.statuses, id)
	return nil
}

type startWorkspace struct{ creates int }

func (w *startWorkspace) Create(_ context.Context, t core.Task) (core.Workspace, error) {
	w.creates++
	return core.Workspace{
		Path:   filepath.Join("/ws", string(t.ID)),
		Branch: "beacon/" + string(t.ID),
	}, nil
}

func (*startWorkspace) Clean(context.Context, core.Workspace) (bool, error) { return false, nil }
func (*startWorkspace) Remove(context.Context, core.Workspace) error        { return nil }
func (*startWorkspace) ForceRemove(context.Context, core.Workspace) error   { return nil }
func (*startWorkspace) HasChanges(context.Context, core.Workspace, string) (bool, error) {
	return false, nil
}
func (*startWorkspace) CommitPath(context.Context, core.Workspace, string, string) error {
	return nil
}

type startHarness struct{ spawns int }

func (h *startHarness) Spawn(_ context.Context, req core.SpawnRequest) (core.Session, error) {
	h.spawns++
	return core.Session{ID: "ses-" + string(req.RunID)}, nil
}

func (*startHarness) Wake(context.Context, string, core.WakeContext) error { return nil }
func (*startHarness) Stop(context.Context, string) error                   { return nil }
func (*startHarness) IsAlive(context.Context, string) (bool, error)        { return false, nil }
func (*startHarness) NotifyParent(context.Context, string, string) error   { return nil }
func (*startHarness) Events() <-chan core.HarnessEvent                     { return nil }

type startFixture struct {
	client    *msdk.ClientSession
	source    *startSource
	store     *startStore
	workspace *startWorkspace
	harness   *startHarness
}

func newStartFixture(t *testing.T, tasks []core.Task, active []*core.Run, conc core.Concurrency) *startFixture {
	t.Helper()
	f := &startFixture{
		source:    &startSource{tasks: tasks},
		store:     &startStore{active: active},
		workspace: &startWorkspace{},
		harness:   &startHarness{},
	}
	d := &app.Dispatcher{
		Source: f.source, Planner: planner.New(planner.Options{}),
		Workspace: f.workspace, Harness: f.harness, Store: f.store,
		Concurrency: conc,
	}
	f.client = deleteClient(t, Services{Dispatcher: d})
	return f
}

func callStart(t *testing.T, f *startFixture, id string) app.StartOneOutcome {
	t.Helper()
	res, err := f.client.CallTool(context.Background(), &msdk.CallToolParams{
		Name: "beacon_task_start", Arguments: StartArgs{TaskID: id},
		Meta: msdk.Meta{"beacon/agent": "orchestrator"},
	})
	if err != nil {
		t.Fatalf("CallTool(beacon_task_start): %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %+v", res)
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatalf("unexpected result: %+v", res.Content)
	}
	var out app.StartOneOutcome
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("json.Unmarshal(%s): %v", text.Text, err)
	}
	return out
}

// assertUntouched checks the one invariant every refusal shares: no card was
// marked, no run was saved and no task status was written.
func (f *startFixture) assertUntouched(t *testing.T) {
	t.Helper()
	if len(f.source.marked) != 0 {
		t.Errorf("marked = %v, want none", f.source.marked)
	}
	if len(f.store.saves) != 0 {
		t.Errorf("saved runs = %v, want none", f.store.saves)
	}
	if len(f.store.statuses) != 0 {
		t.Errorf("status writes = %v, want none", f.store.statuses)
	}
	if f.store.syncs != 0 {
		t.Errorf("task syncs = %d, want 0", f.store.syncs)
	}
	if f.workspace.creates != 0 {
		t.Errorf("workspaces = %d, want 0", f.workspace.creates)
	}
	if f.harness.spawns != 0 {
		t.Errorf("spawns = %d, want 0", f.harness.spawns)
	}
}

func startTask(id core.TaskID, deps ...core.TaskID) core.Task {
	return core.Task{
		ID: id, Status: core.TaskAvailable,
		Scope:     []core.ScopePath{core.ScopeFile(string(id) + ".go")},
		DependsOn: deps,
	}
}

func TestTaskStartRefusesUnknownID(t *testing.T) {
	f := newStartFixture(t, []core.Task{startTask("T-A")}, nil, core.Concurrency{})
	out := callStart(t, f, "T-MISSING")
	if out.Code != string(app.StartUnknownTask) || out.RunID != "" {
		t.Fatalf("outcome = %+v, want unknown_task refusal", out)
	}
	if out.TaskID != "T-MISSING" || out.Reason == "" {
		t.Fatalf("refusal lacks task id or reason: %+v", out)
	}
	f.assertUntouched(t)
	if len(f.source.tasks) != 1 || f.source.tasks[0].ID != "T-A" {
		t.Fatalf("task set changed: %+v", f.source.tasks)
	}
}

func TestTaskStartRefusesActiveRun(t *testing.T) {
	active := []*core.Run{{
		ID: "run-active", TaskID: "T-A", State: core.RunRunning,
	}}
	f := newStartFixture(t, []core.Task{startTask("T-A"), startTask("T-B")}, active, core.Concurrency{})
	out := callStart(t, f, "T-A")
	if out.Code != string(app.SkipActiveRun) || out.RunID != "" {
		t.Fatalf("outcome = %+v, want active_run refusal", out)
	}
	if len(out.Blockers) != 1 || out.Blockers[0].TaskID != "T-A" {
		t.Fatalf("blockers = %+v, want the active task", out.Blockers)
	}
	f.assertUntouched(t)
	if len(f.source.tasks) != 2 || f.source.tasks[1].ID != "T-B" {
		t.Fatalf("unrelated task changed: %+v", f.source.tasks)
	}
	if len(f.store.active) != 1 || f.store.active[0].ID != "run-active" {
		t.Fatalf("active runs changed: %+v", f.store.active)
	}
}

func TestTaskStartRefusesUnresolvedDependency(t *testing.T) {
	tasks := []core.Task{startTask("T-A", "T-B"), startTask("T-B")}
	f := newStartFixture(t, tasks, nil, core.Concurrency{})
	out := callStart(t, f, "T-A")
	if out.Code != string(app.StartUnresolvedDeps) || out.RunID != "" {
		t.Fatalf("outcome = %+v, want unresolved_dependencies refusal", out)
	}
	if len(out.Blockers) != 1 || out.Blockers[0].TaskID != "T-B" ||
		out.Blockers[0].Status != string(core.TaskAvailable) {
		t.Fatalf("blockers = %+v, want T-B available", out.Blockers)
	}
	f.assertUntouched(t)
	if f.source.tasks[1].ID != "T-B" || f.source.tasks[1].Status != core.TaskAvailable {
		t.Fatalf("blocker card was changed: %+v", f.source.tasks[1])
	}
}

func TestTaskStartRefusesFullBudget(t *testing.T) {
	tests := []struct {
		name string
		conc core.Concurrency
	}{
		{"in flight full", core.Concurrency{MaxInFlight: 2, MaxActiveAgents: 2}},
		{"agent slots full", core.Concurrency{MaxInFlight: 5, MaxActiveAgents: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			active := []*core.Run{
				{ID: "run-1", TaskID: "T-X", State: core.RunRunning},
				{ID: "run-2", TaskID: "T-Y", State: core.RunRunning},
			}
			f := newStartFixture(t, []core.Task{startTask("T-A")}, active, tc.conc)
			out := callStart(t, f, "T-A")
			if out.Code != string(app.StartNoSlot) || out.RunID != "" {
				t.Fatalf("outcome = %+v, want no_slot refusal", out)
			}
			if out.Active != 2 {
				t.Errorf("active = %d, want 2", out.Active)
			}
			f.assertUntouched(t)
			if len(f.store.active) != 2 || f.store.active[0].ID != "run-1" || f.store.active[1].ID != "run-2" {
				t.Fatalf("active runs changed: %+v", f.store.active)
			}
		})
	}
}

func TestTaskStartStartsExactlyOneTask(t *testing.T) {
	tasks := []core.Task{startTask("T-A"), startTask("T-B")}
	f := newStartFixture(t, tasks, nil, core.Concurrency{})
	out := callStart(t, f, "T-A")
	if out.RunID == "" || out.TaskID != "T-A" || out.State != core.RunRunning {
		t.Fatalf("outcome = %+v, want one running task", out)
	}
	if out.Code != "" || out.Reason != "" {
		t.Fatalf("success carries refusal fields: %+v", out)
	}
	if len(out.Scope) != 1 || out.Scope[0] != core.ScopeFile("T-A.go") {
		t.Fatalf("scope = %v, want T-A.go", out.Scope)
	}
	if len(f.store.saves) != 1 || f.store.saves[0] != out.RunID {
		t.Fatalf("saved runs = %v, want one %s", f.store.saves, out.RunID)
	}
	if len(f.store.statuses) != 1 || f.store.statuses[0] != "T-A" {
		t.Fatalf("status writes = %v, want only T-A", f.store.statuses)
	}
	if len(f.source.marked) != 1 || f.source.marked[0] != "T-A" {
		t.Fatalf("marked = %v, want only T-A", f.source.marked)
	}
	if f.store.syncs != 1 || f.workspace.creates != 1 || f.harness.spawns != 1 {
		t.Fatalf("syncs=%d creates=%d spawns=%d, want one each",
			f.store.syncs, f.workspace.creates, f.harness.spawns)
	}
}

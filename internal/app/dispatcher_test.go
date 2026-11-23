package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

func twentyTasks() []core.Task {
	var tasks []core.Task
	for i := 0; i < 20; i++ {
		tasks = append(tasks, core.Task{
			ID:     core.TaskID(fmt.Sprintf("T-%02d", i)),
			Status: core.TaskAvailable,
			Scope:  []core.ScopePath{core.ScopeFile(fmt.Sprintf("mod%d/file.go", i))},
		})
	}
	return tasks
}

func TestTwoPoolConcurrency(t *testing.T) {
	store := newFakeStore()
	src := &fakeSource{tasks: twentyTasks()}
	h := &fakeHarness{}
	d := &Dispatcher{
		Source: src, Planner: NewPlanner(nil), Workspace: &fakeWorkspace{},
		Harness: h, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 20, MaxActiveAgents: 3},
	}
	res, err := d.Dispatch(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Runs) != 20 {
		t.Fatalf(" dispatched %d runs", len(res.Runs))
	}
	running, pending := 0, 0
	for _, r := range res.Runs {
		switch r.State {
		case core.RunRunning:
			running++
		case core.RunPending:
			pending++
		}
	}
	if running != 3 || pending != 17 {
		t.Fatalf("running=%d pending=%d", running, pending)
	}
	if len(h.sessions) != 3 {
		t.Fatalf("spawned %d agents", len(h.sessions))
	}

	for _, r := range res.Runs {
		if r.State == core.RunRunning {
			if err := r.Apply(core.Event{Kind: core.EvPRRegistered, PRNumber: 42}); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	started, err := d.PromotePending(context.Background())
	if err != nil || len(started) != 1 {
		t.Fatalf("promoted %d runs, err=%v", len(started), err)
	}
	if len(h.sessions) != 4 {
		t.Fatalf("expected 4 agent sessions, got %d", len(h.sessions))
	}
}

func TestDispatchMarksCardInWorkspace(t *testing.T) {
	store := newFakeStore()
	src := &fakeSource{tasks: []core.Task{{ID: "T-MARK", Status: core.TaskAvailable}}}
	d := &Dispatcher{
		Source: src, Planner: NewPlanner(nil), Workspace: &fakeWorkspace{},
		Harness: &fakeHarness{}, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
	}
	if _, err := d.Dispatch(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(src.marked) != 1 || src.marked[0].id != "T-MARK" ||
		src.marked[0].path != "/ws/T-MARK" || src.marked[0].status != "in_progress" {
		t.Fatalf("bad MarkInWorkspace call: %+v", src.marked)
	}
}

func TestDispatchTellsChildAgentTheBaseBranch(t *testing.T) {
	store := newFakeStore()
	tasks := []core.Task{
		{ID: "T-CHILD", Status: core.TaskAvailable, BaseBranch: "beacon/T-EPIC", Scope: []core.ScopePath{core.ScopeFile("child.go")}},
		{ID: "T-PLAIN", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("plain.go")}},
	}
	h := &fakeHarness{}
	d := &Dispatcher{
		Source: &fakeSource{tasks: tasks}, Planner: NewPlanner(nil), Workspace: &fakeWorkspace{},
		Harness: h, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 2, MaxActiveAgents: 2},
	}
	if _, err := d.Dispatch(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if len(h.prompts) != 2 {
		t.Fatalf("prompts = %d, want 2", len(h.prompts))
	}
	promptFor := func(id core.TaskID) string {
		for _, p := range h.prompts {
			if strings.Contains(p, "Task: "+string(id)+" ") {
				return p
			}
		}
		return ""
	}
	child := promptFor("T-CHILD")
	if !strings.Contains(child, "BaseBranch: beacon/T-EPIC") ||
		!strings.Contains(child, "Open the pull request against beacon/T-EPIC") {
		t.Fatalf("child prompt missing base branch: %s", child)
	}
	plain := promptFor("T-PLAIN")
	if strings.Contains(plain, "BaseBranch:") {
		t.Fatalf("plain prompt carries a base branch: %s", plain)
	}
}

func TestDispatchDoesNotRetryActiveTask(t *testing.T) {
	store := newFakeStore()
	tasks := twentyTasks()
	src := &fakeSource{tasks: tasks}
	active := core.NewRun("r-existing", tasks[0].ID, time.Now().UTC())
	active.State = core.RunRunning
	if err := store.Save(context.Background(), active); err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{
		Source: src, Planner: NewPlanner(nil), Workspace: &fakeWorkspace{},
		Harness: &fakeHarness{}, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 20, MaxActiveAgents: 20},
	}
	res, err := d.Dispatch(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Skipped {
		if s.TaskID == tasks[0].ID {
			if s.Code != SkipActiveRun {
				t.Errorf("skip code = %q, want %q", s.Code, SkipActiveRun)
			}
			if s.BlockedBy != tasks[0].ID {
				t.Errorf("blocked_by = %q, want %q", s.BlockedBy, tasks[0].ID)
			}
			if s.BlockedByRun != "r-existing" {
				t.Errorf("blocked_by_run = %q, want r-existing", s.BlockedByRun)
			}
			return
		}
	}
	t.Fatal("active task was not reported as skipped")
}

func TestDispatchReportsStructuredConflicts(t *testing.T) {
	store := newFakeStore()
	tasks := []core.Task{
		{ID: "A", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("internal/infra/mcp/server.go")}},
		{ID: "B", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("internal/infra/mcp/server.go")}},
		{ID: "C", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("docs/tasks/c.md")}},
	}
	active := core.NewRun("r-active", "Z", time.Now().UTC())
	active.State = core.RunRunning
	active.Scope = []core.ScopePath{core.ScopeFile("docs/tasks/c.md")}
	if err := store.Save(context.Background(), active); err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{
		Source: &fakeSource{tasks: tasks}, Planner: NewPlanner(nil), Workspace: &fakeWorkspace{},
		Harness: &fakeHarness{}, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 20, MaxActiveAgents: 20},
	}
	res, err := d.Dispatch(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Runs) != 1 || res.Runs[0].TaskID != "A" {
		t.Fatalf("unexpected runs: %+v", res.Runs)
	}
	byID := map[core.TaskID]SkippedTask{}
	for _, s := range res.Skipped {
		byID[s.TaskID] = s
	}
	if len(byID) != 2 {
		t.Fatalf("expected 2 skipped, got %+v", res.Skipped)
	}

	selectedSkip, ok := byID["B"]
	if !ok {
		t.Fatalf("selected conflict not reported: %+v", res.Skipped)
	}
	if selectedSkip.Code != SkipSelectedScopeOverlap || selectedSkip.BlockedBy != "A" {
		t.Errorf("selected conflict = %+v", selectedSkip)
	}
	if !equalPaths(selectedSkip.Overlap, []core.ScopePath{core.ScopeFile("internal/infra/mcp/server.go")}) {
		t.Errorf("selected conflict overlap = %+v", selectedSkip.Overlap)
	}
	if selectedSkip.Reason != "scope overlaps A on f:internal/infra/mcp/server.go" {
		t.Errorf("selected conflict reason = %q", selectedSkip.Reason)
	}

	activeSkip, ok := byID["C"]
	if !ok {
		t.Fatalf("active conflict not reported: %+v", res.Skipped)
	}
	if activeSkip.Code != SkipActiveScopeOverlap || activeSkip.BlockedBy != "Z" || activeSkip.BlockedByRun != "r-active" {
		t.Errorf("active conflict = %+v", activeSkip)
	}
	if !equalPaths(activeSkip.Overlap, []core.ScopePath{core.ScopeFile("docs/tasks/c.md")}) {
		t.Errorf("active conflict overlap = %+v", activeSkip.Overlap)
	}
}

type failingWorkspace struct {
	*fakeWorkspace
	err error
}

func (f failingWorkspace) Create(context.Context, core.Task) (core.Workspace, error) {
	return core.Workspace{}, f.err
}

type failingHarness struct {
	*fakeHarness
	err error
}

func (f failingHarness) Spawn(context.Context, core.SpawnRequest) (core.Session, error) {
	return core.Session{}, f.err
}

func assertFailureJSON(t *testing.T, failures []DispatchFailure, want string) {
	t.Helper()
	b, err := json.Marshal(failures)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["Err"] != want {
		t.Fatalf("failure JSON = %s, want Err %q", b, want)
	}
}

func TestDispatchFailureJSONIncludesWorkspaceConflict(t *testing.T) {
	want := "workspace: conflict: refusing to remove dirty worktree /ws/T-CONFLICT"
	store := newFakeStore()
	d := &Dispatcher{
		Source:  &fakeSource{tasks: []core.Task{{ID: "T-CONFLICT", Status: core.TaskAvailable}}},
		Planner: NewPlanner(nil),
		Workspace: failingWorkspace{
			fakeWorkspace: &fakeWorkspace{},
			err:           errors.New("conflict: refusing to remove dirty worktree /ws/T-CONFLICT"),
		},
		Harness:     &fakeHarness{},
		Store:       store,
		Concurrency: core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
	}
	res, err := d.Dispatch(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	assertFailureJSON(t, res.Failures, want)
}

func TestDispatchFailureJSONIncludesSpawnTimeout(t *testing.T) {
	want := "spawn: Post http://127.0.0.1:7432/session: context deadline exceeded"
	store := newFakeStore()
	d := &Dispatcher{
		Source:    &fakeSource{tasks: []core.Task{{ID: "T-SPAWN", Status: core.TaskAvailable}}},
		Planner:   NewPlanner(nil),
		Workspace: &fakeWorkspace{},
		Harness: failingHarness{
			fakeHarness: &fakeHarness{},
			err:         errors.New("Post http://127.0.0.1:7432/session: context deadline exceeded"),
		},
		Store:       store,
		Concurrency: core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
	}
	res, err := d.Dispatch(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	assertFailureJSON(t, res.Failures, want)
}

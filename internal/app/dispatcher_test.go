package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
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
		t.Fatalf(`d.Dispatch(context.Background(), 20): %v`, err)
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
				t.Fatalf(`r.Apply(core.Event{Kind: core.EvPRRegistered, PRNumber: 42}): %v`, err)
			}
			if err := store.Save(context.Background(), r); err != nil {
				t.Fatalf(`store.Save(context.Background(), r): %v`, err)
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
		t.Fatalf(`d.Dispatch(context.Background(), 1): %v`, err)
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
		t.Fatalf(`d.Dispatch(context.Background(), 2): %v`, err)
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
		t.Fatalf(`store.Save(context.Background(), active): %v`, err)
	}
	d := &Dispatcher{
		Source: src, Planner: NewPlanner(nil), Workspace: &fakeWorkspace{},
		Harness: &fakeHarness{}, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 20, MaxActiveAgents: 20},
	}
	res, err := d.Dispatch(context.Background(), 20)
	if err != nil {
		t.Fatalf(`d.Dispatch(context.Background(), 20): %v`, err)
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
		t.Fatalf(`store.Save(context.Background(), active): %v`, err)
	}
	d := &Dispatcher{
		Source: &fakeSource{tasks: tasks}, Planner: NewPlanner(nil), Workspace: &fakeWorkspace{},
		Harness: &fakeHarness{}, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 20, MaxActiveAgents: 20},
	}
	res, err := d.Dispatch(context.Background(), 20)
	if err != nil {
		t.Fatalf(`d.Dispatch(context.Background(), 20): %v`, err)
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

type livenessHarness struct {
	*fakeHarness
	alive bool
}

func (h livenessHarness) IsAlive(context.Context, string) (bool, error) { return h.alive, nil }

func assertFailureJSON(t *testing.T, failures []DispatchFailure, want string) {
	t.Helper()
	b, err := json.Marshal(failures)
	if err != nil {
		t.Fatalf(`json.Marshal(failures): %v`, err)
	}
	var got []map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf(`json.Unmarshal(b, &got): %v`, err)
	}
	if len(got) != 1 || got[0]["Err"] != want {
		t.Fatalf("failure JSON = %s, want Err %q", b, want)
	}
}

func TestDispatchFailureJSONIncludesWorkspaceConflict(t *testing.T) {
	want := "workspace: conflict: refusing to remove dirty worktree /ws/T-CONFLICT"
	conflict := core.WorkspaceConflict("/ws/T-CONFLICT")
	if got := core.CodeOf(conflict); got != core.CodeConflict {
		t.Fatalf("core.CodeOf(WorkspaceConflict(...)) = %q, want %q", got, core.CodeConflict)
	}
	store := newFakeStore()
	d := &Dispatcher{
		Source:  &fakeSource{tasks: []core.Task{{ID: "T-CONFLICT", Status: core.TaskAvailable}}},
		Planner: NewPlanner(nil),
		Workspace: failingWorkspace{
			fakeWorkspace: &fakeWorkspace{},
			err:           conflict,
		},
		Harness:     &fakeHarness{},
		Store:       store,
		Concurrency: core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
	}
	res, err := d.Dispatch(context.Background(), 1)
	if err != nil {
		t.Fatalf(`d.Dispatch(context.Background(), 1): %v`, err)
	}
	assertFailureJSON(t, res.Failures, want)
}

func TestDispatchKeepsRunWithLiveWorkerAfterStatusFailure(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.taskStatusErr = errors.New("status rejected")
	src := &fakeSource{tasks: []core.Task{{ID: "T-LIVE", Status: core.TaskAvailable}}}
	h := &livenessHarness{fakeHarness: &fakeHarness{}, alive: true}
	ws := &fakeWorkspace{}
	d := &Dispatcher{
		Source: src, Planner: NewPlanner(nil), Workspace: ws, Harness: h, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
	}
	res, err := d.Dispatch(ctx, 1)
	if err != nil {
		t.Fatalf(`d.Dispatch(ctx, 1): %v`, err)
	}
	if len(res.Failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(res.Failures))
	}
	active, err := store.ListActive(ctx)
	if err != nil || len(active) != 1 {
		t.Fatalf("active runs = %d, err=%v, want 1", len(active), err)
	}
	run := active[0]
	saved := store.runs[run.ID]
	if saved.State != core.RunRunning || saved.SessionID == "" {
		t.Fatalf("state=%s session=%q, want running with a session", saved.State, saved.SessionID)
	}
	if saved.WorkspacePath == "" || ws.removed != 0 || h.stops != 0 {
		t.Fatalf("live worker cleaned up: path=%q removed=%d stops=%d", saved.WorkspacePath, ws.removed, h.stops)
	}

	c := &Control{Store: store, Harness: h, Workspace: ws}
	if err := c.WatchPR(ctx, run.ID, 43); err != nil {
		t.Fatalf(`c.WatchPR(ctx, run.ID, 43): %v`, err)
	}
	if bound := store.runs[run.ID]; bound.State != core.RunWaitingCI || bound.PRNumber != 43 {
		t.Fatalf("state=%s pr=%d, want %s/43", bound.State, bound.PRNumber, core.RunWaitingCI)
	}
}

func TestDispatchClosesRunWhenWorkerIsGone(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.taskStatusErr = errors.New("status rejected")
	src := &fakeSource{tasks: []core.Task{{ID: "T-DEAD", Status: core.TaskAvailable}}}
	h := &livenessHarness{fakeHarness: &fakeHarness{}, alive: false}
	ws := &fakeWorkspace{}
	d := &Dispatcher{
		Source: src, Planner: NewPlanner(nil), Workspace: ws, Harness: h, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
	}
	if _, err := d.Dispatch(ctx, 1); err != nil {
		t.Fatalf(`d.Dispatch(ctx, 1): %v`, err)
	}
	recent, err := store.ListRecent(ctx, 10)
	if err != nil || len(recent) != 1 {
		t.Fatalf("recent runs = %d, err=%v, want 1", len(recent), err)
	}
	saved := store.runs[recent[0].ID]
	if saved.State != core.RunClosed || saved.Reason != "status rejected" {
		t.Fatalf("state=%s reason=%q, want closed with cause", saved.State, saved.Reason)
	}
	if h.stops != 1 || ws.forcedRemoves != 1 || ws.removed != 0 {
		t.Fatalf("stops=%d forcedRemoves=%d removals=%d, want 1/1/0", h.stops, ws.forcedRemoves, ws.removed)
	}
}

func TestDispatchSpawnFailureForceRemovesDirtyWorktree(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	ws := &fakeWorkspace{dirty: true}
	d := &Dispatcher{
		Source:    &fakeSource{tasks: []core.Task{{ID: "T-SPAWN-DIRTY", Status: core.TaskAvailable}}},
		Planner:   NewPlanner(nil),
		Workspace: ws,
		Harness: failingHarness{
			fakeHarness: &fakeHarness{},
			err:         errors.New("spawn exploded"),
		},
		Store:       store,
		Concurrency: core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
	}
	res, err := d.Dispatch(ctx, 1)
	if err != nil {
		t.Fatalf(`d.Dispatch(ctx, 1): %v`, err)
	}
	if len(res.Failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(res.Failures))
	}
	if ws.forcedRemoves != 1 || ws.removed != 0 {
		t.Fatalf("forcedRemoves = %d, removed = %d; want 1 and 0", ws.forcedRemoves, ws.removed)
	}
}

func TestDispatchRetriesTaskAfterSpawnFailureForceRemoval(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	ws := &fakeWorkspace{dirty: true}
	src := &fakeSource{tasks: []core.Task{{ID: "T-RETRY", Status: core.TaskAvailable}}}
	d := &Dispatcher{
		Source: src, Planner: NewPlanner(nil), Workspace: ws, Store: store,
		Harness:     failingHarness{fakeHarness: &fakeHarness{}, err: errors.New("spawn exploded")},
		Concurrency: core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
	}
	first, err := d.Dispatch(ctx, 1)
	if err != nil || len(first.Failures) != 1 {
		t.Fatalf("first dispatch: %+v, err=%v", first, err)
	}
	if ws.forcedRemoves != 1 || ws.removed != 0 {
		t.Fatalf("first cleanup: force=%d remove=%d, want 1/0", ws.forcedRemoves, ws.removed)
	}

	d.Harness = &fakeHarness{}
	second, err := d.Dispatch(ctx, 1)
	if err != nil {
		t.Fatalf(`d.Dispatch(ctx, 1): %v`, err)
	}
	if len(second.Failures) != 0 || len(second.Runs) != 1 {
		t.Fatalf("second dispatch: %+v, want one started run", second)
	}
	active, err := store.ListActive(ctx)
	if err != nil || len(active) != 1 {
		t.Fatalf("active runs = %d, err=%v, want 1", len(active), err)
	}
	if len(src.marked) != 2 || src.marked[1].status != "in_progress" {
		t.Fatalf("MarkInWorkspace calls = %+v", src.marked)
	}
	if len(src.claimed) != 0 {
		t.Fatalf("root source was claimed %d times, want 0", len(src.claimed))
	}
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
		t.Fatalf(`d.Dispatch(context.Background(), 1): %v`, err)
	}
	assertFailureJSON(t, res.Failures, want)
}

func newInstructionDispatcher(h *requestCapturingHarness, instructions string) *Dispatcher {
	return &Dispatcher{
		Source: &fakeSource{tasks: []core.Task{{ID: "T-PREFIX", Status: core.TaskAvailable,
			Scope: []core.ScopePath{core.ScopeFile("prefix.go")}}},
		},
		Planner:            NewPlanner(nil),
		Workspace:          &fakeWorkspace{},
		Harness:            h,
		Store:              newFakeStore(),
		Concurrency:        core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
		WorkerInstructions: instructions,
	}
}

func TestDispatchPrependsWorkerInstructions(t *testing.T) {
	const custom = "Custom worker instructions from config."
	h := &requestCapturingHarness{fakeHarness: &fakeHarness{}}
	d := newInstructionDispatcher(h, custom)
	if _, err := d.Dispatch(context.Background(), 1); err != nil {
		t.Fatalf(`d.Dispatch(context.Background(), 1): %v`, err)
	}
	if len(h.requests) != 1 {
		t.Fatalf("spawn requests = %d, want 1", len(h.requests))
	}
	prompt := h.requests[0].Prompt
	if !strings.HasPrefix(prompt, custom+"\n\n") {
		t.Fatalf("prompt does not start with configured instructions: %q", prompt)
	}
	if !strings.Contains(prompt, "Implement the task, open a pull request, then call beacon_watch_pr") {
		t.Fatalf("stock prompt content lost: %q", prompt)
	}
}

func TestDispatchWithoutWorkerInstructionsKeepsStockPrompt(t *testing.T) {
	h := &requestCapturingHarness{fakeHarness: &fakeHarness{}}
	d := newInstructionDispatcher(h, "")
	if _, err := d.Dispatch(context.Background(), 1); err != nil {
		t.Fatalf(`d.Dispatch(context.Background(), 1): %v`, err)
	}
	if len(h.requests) != 1 {
		t.Fatalf("spawn requests = %d, want 1", len(h.requests))
	}
	prompt := h.requests[0].Prompt
	if !strings.HasPrefix(prompt, "Run ID: ") {
		t.Fatalf("prompt gained an unexpected prefix: %q", prompt)
	}
	if !strings.Contains(prompt, "Implement the task, open a pull request, then call beacon_watch_pr") {
		t.Fatalf("stock prompt content lost: %q", prompt)
	}
}

func TestSpawnPromptRequiresPrePRFormatAndTestCheck(t *testing.T) {
	h := &requestCapturingHarness{fakeHarness: &fakeHarness{}}
	d := newInstructionDispatcher(h, "")
	if _, err := d.Dispatch(context.Background(), 1); err != nil {
		t.Fatalf(`d.Dispatch(context.Background(), 1): %v`, err)
	}
	if len(h.requests) != 1 {
		t.Fatalf("spawn requests = %d, want 1", len(h.requests))
	}
	prompt := h.requests[0].Prompt
	for _, want := range []string{
		"gofmt -l",
		"go test",
		"Do not open a pull request",
		"Report the exact command you ran and its full output",
		"advisory",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("spawn prompt missing %q: %s", want, prompt)
		}
	}
}

func TestSpawnPromptSharesWeakMachineRule(t *testing.T) {
	h := &requestCapturingHarness{fakeHarness: &fakeHarness{}}
	d := newInstructionDispatcher(h, "")
	if _, err := d.Dispatch(context.Background(), 1); err != nil {
		t.Fatalf(`d.Dispatch(context.Background(), 1): %v`, err)
	}
	if len(h.requests) != 1 {
		t.Fatalf("spawn requests = %d, want 1", len(h.requests))
	}
	if !strings.Contains(h.requests[0].Prompt, core.WeakMachineVerificationRule) {
		t.Fatalf("spawn prompt does not state the shared rule: %q", h.requests[0].Prompt)
	}
}

func TestDispatchResolvesModelFromParentSession(t *testing.T) {
	ref := core.ModelRef{ID: "hy4-flash-free", ProviderID: "opencode"}
	var parents []string
	h := &requestCapturingHarness{fakeHarness: &fakeHarness{}}
	d := newInstructionDispatcher(h, "")
	d.ParentSessionID = "ses_fallback"
	d.ResolveModel = func(parent string) core.ModelRef {
		parents = append(parents, parent)
		return ref
	}
	ctx := WithParentSession(context.Background(), "ses_parent")
	if _, err := d.Dispatch(ctx, 1); err != nil {
		t.Fatalf(`d.Dispatch(ctx, 1): %v`, err)
	}
	if len(parents) != 1 || parents[0] != "ses_parent" {
		t.Fatalf("resolved parents = %v, want [ses_parent]", parents)
	}
	if len(h.requests) != 1 {
		t.Fatalf("spawn requests = %d, want 1", len(h.requests))
	}
	req := h.requests[0]
	if !reflect.DeepEqual(req.ModelRef, ref) {
		t.Fatalf("spawn model = %v, want %v", req.ModelRef, ref)
	}
	if req.ParentSessionID != "ses_parent" {
		t.Fatalf("spawn parent = %q, want ses_parent", req.ParentSessionID)
	}
}

func TestDispatchResolvesModelForTwoCards(t *testing.T) {
	tasks := []core.Task{
		{ID: "T-MODEL-1", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("m1.go")}},
		{ID: "T-MODEL-2", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("m2.go")}},
	}
	h := &requestCapturingHarness{fakeHarness: &fakeHarness{}}
	d := &Dispatcher{
		Source: &fakeSource{tasks: tasks}, Planner: NewPlanner(nil), Workspace: &fakeWorkspace{},
		Harness: h, Store: newFakeStore(),
		Concurrency:     core.Concurrency{MaxInFlight: 2, MaxActiveAgents: 2},
		ParentSessionID: "ses_parent",
	}
	d.ResolveModel = func(parent string) core.ModelRef {
		return core.ModelRef{ProviderID: "opencode", ID: "model-x"}
	}
	ctx := WithParentSession(context.Background(), "ses_parent")
	res, err := d.Dispatch(ctx, 2)
	if err != nil {
		t.Fatalf(`d.Dispatch(ctx, 2): %v`, err)
	}
	if len(res.Runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(res.Runs))
	}
	for _, req := range h.requests {
		if req.ModelRef.ID != "model-x" || req.ParentSessionID != "ses_parent" {
			t.Fatalf("spawn request = %+v, want the resolved model and parent", req)
		}
	}
}

func TestDispatchWithoutResolverLeavesHarnessDefault(t *testing.T) {
	h := &requestCapturingHarness{fakeHarness: &fakeHarness{}}
	d := newInstructionDispatcher(h, "")
	if _, err := d.Dispatch(context.Background(), 1); err != nil {
		t.Fatalf(`d.Dispatch(context.Background(), 1): %v`, err)
	}
	if len(h.requests) != 1 {
		t.Fatalf("spawn requests = %d, want 1", len(h.requests))
	}
	if !h.requests[0].ModelRef.IsZero() {
		t.Fatalf("spawn model = %v, want no model without a resolver", h.requests[0].ModelRef)
	}
}

func TestFixerPromptPrependsWorkerInstructions(t *testing.T) {
	const custom = "Custom worker instructions from config."
	in := &core.BaseIncident{FailedChecksJSON: "[\"ci\"]"}
	prompt := fixerPrompt(redBase(), in, core.Task{ID: "FIX-1"}, custom)
	if !strings.HasPrefix(prompt, custom+"\n\n") {
		t.Fatalf("fixer prompt does not start with configured instructions: %q", prompt)
	}
	if !strings.Contains(prompt, "Base branch main is red") {
		t.Fatalf("fixer prompt content lost: %q", prompt)
	}
}

func TestDispatchEnsuresMissingEpicBranchBeforeWorkspace(t *testing.T) {
	ws := &branchWorkspace{fakeWorkspace: &fakeWorkspace{log: &callLog{}}}
	h := &fakeHarness{}
	d := &Dispatcher{
		Source: &fakeSource{tasks: []core.Task{{
			ID: "T-CHILD", Status: core.TaskAvailable, BaseBranch: "beacon/T-EPIC",
		}}},
		Planner: NewPlanner(nil), Workspace: ws, Harness: h, Store: newFakeStore(),
		Concurrency: core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
	}
	res, err := d.Dispatch(context.Background(), 1)
	if err != nil {
		t.Fatalf(`d.Dispatch(context.Background(), 1): %v`, err)
	}
	if len(res.Failures) != 0 {
		t.Fatalf("failures = %+v", res.Failures)
	}
	if len(ws.branches) != 1 || ws.branches[0] != "beacon/T-EPIC" {
		t.Fatalf("branches = %v, want beacon/T-EPIC", ws.branches)
	}
	if len(ws.baseBranches) != 1 || ws.baseBranches[0] != "" {
		t.Fatalf("base branches = %v, want the configured workspace base", ws.baseBranches)
	}
	if len(ws.log.items) != 2 || ws.log.items[0] != "branch:beacon/T-EPIC" ||
		ws.log.items[1] != "create:T-CHILD" {
		t.Fatalf("call order = %v, want branch before workspace create", ws.log.items)
	}
}

func TestDispatchReportsBranchCreationFailure(t *testing.T) {
	want := errors.New("push failed")
	ws := &branchWorkspace{fakeWorkspace: &fakeWorkspace{log: &callLog{}}, branchErr: want}
	h := &fakeHarness{}
	d := &Dispatcher{
		Source: &fakeSource{tasks: []core.Task{{
			ID: "T-CHILD", Status: core.TaskAvailable, BaseBranch: "beacon/T-EPIC",
		}}},
		Planner: NewPlanner(nil), Workspace: ws, Harness: h, Store: newFakeStore(),
		Concurrency: core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
	}
	res, err := d.Dispatch(context.Background(), 1)
	if err != nil {
		t.Fatalf(`d.Dispatch(context.Background(), 1): %v`, err)
	}
	if len(res.Failures) != 1 || !strings.Contains(res.Failures[0].Err, "base branch: push failed") {
		t.Fatalf("failures = %+v, want the branch error", res.Failures)
	}
	if ws.creates != 0 || len(h.sessions) != 0 {
		t.Fatalf("creates = %d, sessions = %d, want none after a branch failure", ws.creates, len(h.sessions))
	}
}

func TestDispatchLeavesMainTasksWithoutBranchWork(t *testing.T) {
	ws := &branchWorkspace{fakeWorkspace: &fakeWorkspace{log: &callLog{}}}
	d := &Dispatcher{
		Source:  &fakeSource{tasks: []core.Task{{ID: "T-PLAIN", Status: core.TaskAvailable}}},
		Planner: NewPlanner(nil), Workspace: ws, Harness: &fakeHarness{}, Store: newFakeStore(),
		Concurrency: core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
	}
	if _, err := d.Dispatch(context.Background(), 1); err != nil {
		t.Fatalf(`d.Dispatch(context.Background(), 1): %v`, err)
	}
	if len(ws.branches) != 0 || ws.creates != 1 {
		t.Fatalf("branches = %v, creates = %d, want no branch work", ws.branches, ws.creates)
	}
}

type listActiveErrorStore struct {
	*fakeStore
}

func (s *listActiveErrorStore) ListActive(context.Context) ([]*core.Run, error) {
	return nil, errors.New("store unavailable")
}

func TestAgentSlotsAvailableCountsActiveAgents(t *testing.T) {
	store := newFakeStore()
	running := core.NewRun("r-run", "T-1", time.Now().UTC())
	running.State = core.RunRunning
	pending := core.NewRun("r-pending", "T-2", time.Now().UTC())
	pending.State = core.RunPending
	for _, run := range []*core.Run{running, pending} {
		if err := store.Save(context.Background(), run); err != nil {
			t.Fatalf(`store.Save(context.Background(), run): %v`, err)
		}
	}
	d := &Dispatcher{Store: store, Concurrency: core.Concurrency{MaxInFlight: 20, MaxActiveAgents: 2}}
	if got := d.Available(context.Background()); got != 1 {
		t.Fatalf("Available = %d, want 1 (pending runs do not hold a slot)", got)
	}
	second := core.NewRun("r-run-2", "T-3", time.Now().UTC())
	second.State = core.RunNeedsAgentFix
	if err := store.Save(context.Background(), second); err != nil {
		t.Fatalf(`store.Save(context.Background(), second): %v`, err)
	}
	if got := d.Available(context.Background()); got != 0 {
		t.Fatalf("Available = %d, want 0 after two active agents", got)
	}
}

func TestAgentSlotsAvailableDefaultsToInFlightLimit(t *testing.T) {
	empty := newFakeStore()
	for _, tc := range []struct {
		name string
		conf core.Concurrency
		want int
	}{
		{"configured in-flight", core.Concurrency{MaxInFlight: 3}, 3},
		{"unconfigured", core.Concurrency{}, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Dispatcher{Store: empty, Concurrency: tc.conf}
			if got := d.Available(context.Background()); got != tc.want {
				t.Fatalf("Available = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAgentSlotsAvailableFailsClosed(t *testing.T) {
	d := &Dispatcher{
		Store:       &listActiveErrorStore{fakeStore: newFakeStore()},
		Concurrency: core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
	}
	if got := d.Available(context.Background()); got != 0 {
		t.Fatalf("Available = %d, want 0 on a store error", got)
	}
}

func TestConcurrentDispatchAndPromotionRespectMaxActiveAgents(t *testing.T) {
	const maxAgents = 3
	store := newFakeStore()
	h := &fakeHarness{}
	d := &Dispatcher{
		Source: &fakeSource{tasks: twentyTasks()}, Planner: NewPlanner(nil),
		Workspace: &fakeWorkspace{}, Harness: h, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 20, MaxActiveAgents: maxAgents},
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := context.Background()
			if i%2 == 0 {
				if _, err := d.Dispatch(ctx, 20); err != nil {
					t.Errorf("dispatch: %v", err)
				}
				return
			}
			if _, err := d.PromotePending(ctx); err != nil {
				t.Errorf("promote: %v", err)
			}
		}(i)
	}
	wg.Wait()

	active, err := store.ListActive(context.Background())
	if err != nil {
		t.Fatalf(`store.ListActive(context.Background()): %v`, err)
	}
	agents := 0
	for _, r := range active {
		if r.State.ActiveAgent() {
			agents++
		}
	}
	if agents != maxAgents {
		t.Fatalf("active agents = %d, want %d", agents, maxAgents)
	}
	if len(h.sessions) != maxAgents {
		t.Fatalf("spawned agents = %d, want %d", len(h.sessions), maxAgents)
	}
}

// benchDelay waits for d or until ctx is cancelled. Every fake step uses it so
// a measured call spends its time where a real dispatch does.
func benchDelay(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Profile delays stand in for the network and session work measured on the
// operator's machine: a remote branch lookup, a base ref fetch, a card rewrite,
// a session spawn and two store writes.
const (
	profileEnsureDelay = 15 * time.Millisecond
	profileCreateDelay = 8 * time.Millisecond
	profileMarkDelay   = 4 * time.Millisecond
	profileSpawnDelay  = 10 * time.Millisecond
	profileSaveDelay   = 4 * time.Millisecond
	profileStatusDelay = 4 * time.Millisecond
)

type timedWorkspace struct {
	*fakeWorkspace
	ensureDelay time.Duration
	createDelay time.Duration
	ensures     int
}

func (w *timedWorkspace) EnsureBranch(ctx context.Context, branch, base string) error {
	w.ensures++
	return benchDelay(ctx, w.ensureDelay)
}

func (w *timedWorkspace) Create(ctx context.Context, task core.Task) (core.Workspace, error) {
	if err := benchDelay(ctx, w.createDelay); err != nil {
		return core.Workspace{}, err
	}
	return w.fakeWorkspace.Create(ctx, task)
}

type timedSource struct {
	*fakeSource
	markDelay time.Duration
}

func (s *timedSource) MarkInWorkspace(ctx context.Context, id core.TaskID, path, status string) error {
	if err := benchDelay(ctx, s.markDelay); err != nil {
		return err
	}
	return s.fakeSource.MarkInWorkspace(ctx, id, path, status)
}

type timedStore struct {
	*fakeStore
	saveDelay   time.Duration
	statusDelay time.Duration
	saves       int
	statuses    int
}

func (s *timedStore) Save(ctx context.Context, run *core.Run) error {
	s.saves++
	if err := benchDelay(ctx, s.saveDelay); err != nil {
		return err
	}
	return s.fakeStore.Save(ctx, run)
}

func (s *timedStore) SetTaskStatus(ctx context.Context, id core.TaskID, status string) error {
	s.statuses++
	if err := benchDelay(ctx, s.statusDelay); err != nil {
		return err
	}
	return s.fakeStore.SetTaskStatus(ctx, id, status)
}

type timedHarness struct {
	*fakeHarness
	spawnDelay time.Duration
}

func (h *timedHarness) Spawn(ctx context.Context, req core.SpawnRequest) (core.Session, error) {
	if err := benchDelay(ctx, h.spawnDelay); err != nil {
		return core.Session{}, err
	}
	return h.fakeHarness.Spawn(ctx, req)
}

type profileDeps struct {
	d       *Dispatcher
	ws      *timedWorkspace
	src     *timedSource
	store   *timedStore
	harness *timedHarness
	spans   *DispatchSpans
}

func profileTasks(n int) []core.Task {
	tasks := make([]core.Task, n)
	for i := range tasks {
		tasks[i] = core.Task{
			ID:         core.TaskID(fmt.Sprintf("T-CHILD-%02d", i)),
			Status:     core.TaskAvailable,
			BaseBranch: "beacon/T-EPIC",
			Scope:      []core.ScopePath{core.ScopeFile(fmt.Sprintf("child%d.go", i))},
		}
	}
	return tasks
}

func newProfileDeps(tasks []core.Task) *profileDeps {
	ws := &timedWorkspace{
		fakeWorkspace: &fakeWorkspace{},
		ensureDelay:   profileEnsureDelay,
		createDelay:   profileCreateDelay,
	}
	src := &timedSource{fakeSource: &fakeSource{tasks: tasks}, markDelay: profileMarkDelay}
	store := &timedStore{
		fakeStore:   newFakeStore(),
		saveDelay:   profileSaveDelay,
		statusDelay: profileStatusDelay,
	}
	harness := &timedHarness{fakeHarness: &fakeHarness{}, spawnDelay: profileSpawnDelay}
	spans := &DispatchSpans{}
	d := &Dispatcher{
		Source: src, Planner: NewPlanner(nil), Workspace: ws,
		Harness: harness, Store: store, Spans: spans,
		Concurrency: core.Concurrency{MaxInFlight: len(tasks), MaxActiveAgents: len(tasks)},
	}
	return &profileDeps{d: d, ws: ws, src: src, store: store, harness: harness, spans: spans}
}

// baselineOne is the pre-change step order. It calls ensureBranch on every
// task and records into the same spans so the two sides are comparable.
func baselineOne(ctx context.Context, deps *profileDeps, run *core.Run, task core.Task) error {
	d := deps.d
	ctx = WithCycleID(ctx, string(run.ID))
	if err := d.timed(ctx, "ensureBranch", func() error {
		return ensureBranch(ctx, d.Workspace, task.BaseBranch, "")
	}); err != nil {
		return fmt.Errorf("base branch: %w", err)
	}
	var ws core.Workspace
	if err := d.timed(ctx, "Create", func() (err error) {
		ws, err = d.Workspace.Create(ctx, task)
		return err
	}); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	run.WorkspacePath = ws.Path
	run.Branch = ws.Branch
	run.Scope = task.Scope
	if err := d.timed(ctx, "MarkInWorkspace", func() error {
		return d.Source.MarkInWorkspace(ctx, task.ID, ws.Path, "in_progress")
	}); err != nil {
		return fmt.Errorf("mark in workspace: %w", err)
	}
	var session core.Session
	if err := d.timed(ctx, "Spawn", func() (err error) {
		session, err = d.Harness.Spawn(ctx, core.SpawnRequest{
			Task: task, Workspace: ws, RunID: run.ID,
			ParentSessionID: run.ParentSessionID, IsFixer: run.IsFixer,
			ModelRef: d.modelRef(run.ParentSessionID),
			Prompt:   spawnPrompt(task, run.ID, ws.Path, d.WorkerInstructions),
		})
		return err
	}); err != nil {
		return fmt.Errorf("spawn: %w", err)
	}
	run.SessionID = session.ID
	if err := run.Apply(core.Event{Kind: core.EvStart}); err != nil {
		return err
	}
	if err := d.timed(ctx, "Save", func() error {
		return d.Store.Save(ctx, run)
	}); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	return d.timed(ctx, "SetTaskStatus", func() error {
		return d.Store.SetTaskStatus(ctx, task.ID, "claimed")
	})
}

func baselineBatch(ctx context.Context, deps *profileDeps, tasks []core.Task) {
	for _, task := range tasks {
		run := core.NewRun(newRunID(), task.ID, time.Now().UTC())
		run.Scope = task.Scope
		if err := deps.store.Save(ctx, run); err != nil {
			panic(err)
		}
		if err := baselineOne(ctx, deps, run, task); err != nil {
			panic(err)
		}
	}
}

func spanTotal(spans *DispatchSpans) time.Duration {
	var total time.Duration
	for _, name := range spans.Steps() {
		for _, d := range spans.Durations(name) {
			total += d
		}
	}
	return total
}

func medianDuration(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

func rangeDuration(values []time.Duration) (time.Duration, time.Duration) {
	lo, hi := values[0], values[0]
	for _, v := range values[1:] {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	return lo, hi
}

func TestDispatchSpansCoverEveryStep(t *testing.T) {
	tasks := profileTasks(2)
	deps := newProfileDeps(tasks)
	res, err := deps.d.Dispatch(context.Background(), len(tasks))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(res.Runs) != len(tasks) {
		t.Fatalf("runs = %d, want %d", len(res.Runs), len(tasks))
	}
	want := []string{"Create", "MarkInWorkspace", "Save", "SetTaskStatus", "Spawn", "ensureBranch"}
	got := deps.spans.Steps()
	if len(got) != len(want) {
		t.Fatalf("spans = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("spans = %v, want %v", got, want)
		}
		if n := len(deps.spans.Durations(want[i])); n != len(tasks) {
			t.Fatalf("%s recorded %d times, want %d", want[i], n, len(tasks))
		}
	}
	if deps.store.saves != len(tasks) {
		t.Fatalf("saves = %d, want %d", deps.store.saves, len(tasks))
	}
	if deps.store.statuses != len(tasks) || len(deps.src.fakeSource.marked) != len(tasks) {
		t.Fatalf("status writes = %d, marks = %v", deps.store.statuses, deps.src.fakeSource.marked)
	}
}

func TestDispatchPersistsPendingRunsOnce(t *testing.T) {
	store := newFakeStore()
	d := &Dispatcher{
		Source: &fakeSource{tasks: twentyTasks()}, Planner: NewPlanner(nil),
		Workspace: &fakeWorkspace{}, Harness: &fakeHarness{}, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 20, MaxActiveAgents: 1},
	}
	res, err := d.Dispatch(context.Background(), 20)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	active, err := store.ListActive(context.Background())
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != len(res.Runs) {
		t.Fatalf("persisted runs = %d, dispatched = %d", len(active), len(res.Runs))
	}
	pending := 0
	for _, r := range active {
		if r.State == core.RunPending {
			pending++
		}
	}
	if pending != len(res.Runs)-1 {
		t.Fatalf("pending runs = %d, want %d", pending, len(res.Runs)-1)
	}
}

func TestDispatchEnsureBranchOncePerBatch(t *testing.T) {
	tasks := profileTasks(5)
	deps := newProfileDeps(tasks)
	if _, err := deps.d.Dispatch(context.Background(), len(tasks)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if deps.ws.ensures != 1 {
		t.Fatalf("EnsureBranch calls = %d, want 1", deps.ws.ensures)
	}
	if len(deps.spans.Durations("ensureBranch")) != len(tasks) {
		t.Fatalf("ensureBranch spans = %d, want %d", len(deps.spans.Durations("ensureBranch")), len(tasks))
	}
}

// TestDispatchLatencyProfile runs five alternating before/after pairs at two
// work counts. It prints each step median and the total range, then checks
// that the batch totals do not overlap.
func TestDispatchLatencyProfile(t *testing.T) {
	const pairs = 5
	steps := []string{"ensureBranch", "Create", "MarkInWorkspace", "Spawn", "Save", "SetTaskStatus"}
	type side struct {
		totals []time.Duration
		byStep map[string][]time.Duration
	}
	// One task has no redundancy to remove and acts as a control. Five
	// children of one epic share the base branch the old code re-checked.
	for _, count := range []int{1, 5} {
		before, after := side{byStep: map[string][]time.Duration{}}, side{byStep: map[string][]time.Duration{}}
		tasks := profileTasks(count)
		for pair := 0; pair < pairs; pair++ {
			for _, mode := range []string{"before", "after"} {
				deps := newProfileDeps(tasks)
				ctx := context.Background()
				if mode == "before" {
					baselineBatch(ctx, deps, tasks)
				} else if _, err := deps.d.Dispatch(ctx, len(tasks)); err != nil {
					t.Fatalf("Dispatch: %v", err)
				}
				s := &before
				if mode == "after" {
					s = &after
				}
				s.totals = append(s.totals, spanTotal(deps.spans))
				for _, step := range steps {
					s.byStep[step] = append(s.byStep[step], deps.spans.Durations(step)...)
				}
			}
		}
		fmt.Printf("\ndispatch profile, work count %d, %d alternating pairs\n", count, pairs)
		fmt.Printf("%-18s %12s %12s\n", "step", "before med", "after med")
		for _, step := range steps {
			fmt.Printf("%-18s %12s %12s\n", step,
				medianDuration(before.byStep[step]), medianDuration(after.byStep[step]))
		}
		bLo, bHi := rangeDuration(before.totals)
		aLo, aHi := rangeDuration(after.totals)
		fmt.Printf("%-18s %12s %12s\n", "total med", medianDuration(before.totals), medianDuration(after.totals))
		fmt.Printf("before range %s to %s, after range %s to %s\n", bLo, bHi, aLo, aHi)
		if count > 1 && aHi >= bLo {
			t.Fatalf("ranges overlap at work count %d: before %s to %s, after %s to %s",
				count, bLo, bHi, aLo, aHi)
		}
	}
}

func BenchmarkDispatchSteps(b *testing.B) {
	tasks := profileTasks(5)
	for _, mode := range []string{"before", "after"} {
		b.Run(mode, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				deps := newProfileDeps(tasks)
				ctx := context.Background()
				if mode == "before" {
					baselineBatch(ctx, deps, tasks)
				} else if _, err := deps.d.Dispatch(ctx, len(tasks)); err != nil {
					b.Fatalf("Dispatch: %v", err)
				}
				b.ReportMetric(float64(spanTotal(deps.spans).Microseconds())/1000, "step-total-ms")
				b.ReportMetric(float64(deps.ws.ensures), "ensure-calls")
			}
		})
	}
}

// failingPlanner records whether a caller asks it to select. The pure helper
// must skip planning when the slot budget is already exhausted.
type failingPlanner struct {
	calls int
}

func (p *failingPlanner) Select([]core.Task, []*core.Run, int, bool) ([]core.Task, []SkippedTask, error) {
	p.calls++
	return nil, nil, errors.New("planner should not be called")
}

func busyRun(id core.RunID, task core.TaskID) *core.Run {
	r := core.NewRun(id, task, time.Now().UTC())
	r.State = core.RunRunning
	return r
}

func TestSelectDispatchCandidatesBudget(t *testing.T) {
	ready := []core.Task{
		{ID: "T-1", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("one.go")}},
		{ID: "T-2", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("two.go")}},
	}
	active := []*core.Run{busyRun("r-1", "R-1"), busyRun("r-2", "R-2")}
	for _, tc := range []struct {
		name       string
		n          int
		inFlight   int
		wantLimit  int
		wantChosen int
	}{
		{"request below free slots", 1, 5, 1, 1},
		{"free slots below request", 4, 3, 1, 1},
		{"no free slots", 4, 2, 0, 0},
		{"negative free slots", 4, 1, -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sel, err := selectDispatchCandidates(NewPlanner(nil), ready, active, tc.n, tc.inFlight, false)
			if err != nil {
				t.Fatalf("selectDispatchCandidates: %v", err)
			}
			if sel.Limit != tc.wantLimit {
				t.Errorf("limit = %d, want %d", sel.Limit, tc.wantLimit)
			}
			if len(sel.Chosen) != tc.wantChosen {
				t.Errorf("chosen = %d, want %d", len(sel.Chosen), tc.wantChosen)
			}
		})
	}
}

func TestSelectDispatchCandidatesSkipsPlannerWithoutBudget(t *testing.T) {
	ready := []core.Task{{ID: "T-1", Status: core.TaskAvailable}}
	active := []*core.Run{busyRun("r-1", "R-1")}
	planner := &failingPlanner{}
	sel, err := selectDispatchCandidates(planner, ready, active, 5, 1, false)
	if err != nil {
		t.Fatalf("selectDispatchCandidates: %v", err)
	}
	if planner.calls != 0 {
		t.Fatalf("planner called %d times with no budget", planner.calls)
	}
	if sel.Limit != 0 || len(sel.Chosen) != 0 || len(sel.Skipped) != 0 {
		t.Fatalf("selection = %+v, want empty", sel)
	}
}

func TestSelectDispatchCandidatesSelection(t *testing.T) {
	ready := []core.Task{
		{ID: "A", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("shared.go")}},
		{ID: "B", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("shared.go")}},
		{ID: "C", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("other.go")}},
	}
	sel, err := selectDispatchCandidates(NewPlanner(nil), ready, nil, 10, 10, false)
	if err != nil {
		t.Fatalf("selectDispatchCandidates: %v", err)
	}
	if sel.Limit != 10 {
		t.Fatalf("limit = %d, want 10", sel.Limit)
	}
	if len(sel.Chosen) != 2 || sel.Chosen[0].ID != "A" || sel.Chosen[1].ID != "C" {
		t.Fatalf("chosen = %v, want [A C]", sel.Chosen)
	}
	if len(sel.Skipped) != 1 {
		t.Fatalf("skipped = %+v, want one conflict", sel.Skipped)
	}
	skip := sel.Skipped[0]
	if skip.TaskID != "B" || skip.Code != SkipSelectedScopeOverlap || skip.BlockedBy != "A" {
		t.Fatalf("skip = %+v, want B blocked by A", skip)
	}
}

func TestSelectDispatchCandidatesPropagatesPlannerError(t *testing.T) {
	planner := &failingPlanner{}
	_, err := selectDispatchCandidates(planner, []core.Task{{ID: "T-1"}}, nil, 1, 1, false)
	if err == nil || err.Error() != "planner should not be called" {
		t.Fatalf("err = %v, want the planner error", err)
	}
	if planner.calls != 1 {
		t.Fatalf("planner calls = %d, want 1", planner.calls)
	}
}

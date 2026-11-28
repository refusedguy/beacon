package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// gatedCreateWorkspace holds the first workspace creation open so two
// dispatchers must both reach the store claim before either run lands.
type gatedCreateWorkspace struct {
	*fakeWorkspace
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (g *gatedCreateWorkspace) Create(ctx context.Context, task core.Task) (core.Workspace, error) {
	g.calls.Add(1)
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.release:
	case <-ctx.Done():
		return core.Workspace{}, ctx.Err()
	}
	return g.fakeWorkspace.Create(ctx, task)
}

func claimTestDispatcher(store *fakeStore, ws core.WorkspaceManager, task core.Task) *Dispatcher {
	return &Dispatcher{
		Source:      &fakeSource{tasks: []core.Task{task}},
		Planner:     NewPlanner(nil),
		Workspace:   ws,
		Harness:     &fakeHarness{},
		Store:       store,
		Concurrency: core.Concurrency{MaxInFlight: 1, MaxActiveAgents: 1},
	}
}

func TestTwoDispatchersClaimOneRunPerTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store := newFakeStore()
	ws := &gatedCreateWorkspace{
		fakeWorkspace: &fakeWorkspace{},
		entered:       make(chan struct{}),
		release:       make(chan struct{}),
	}
	task := core.Task{
		ID: "T-RACE", Status: core.TaskAvailable,
		Scope: []core.ScopePath{core.ScopeFile("race.go")},
	}
	d1 := claimTestDispatcher(store, ws, task)
	d2 := claimTestDispatcher(store, ws, task)

	start := make(chan struct{})
	done := make(chan int, 2)
	for i, d := range []*Dispatcher{d1, d2} {
		go func(i int, d *Dispatcher) {
			<-start
			if _, err := d.Dispatch(ctx, 1); err != nil {
				t.Errorf("dispatcher %d: %v", i, err)
			}
			done <- i
		}(i, d)
	}
	close(start)

	select {
	case <-ws.entered:
	case <-ctx.Done():
		t.Fatal("no dispatcher reached workspace creation")
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the claim loser did not finish")
	}

	// The result that finished first is the one refused at the claim. The
	// winner is still inside the gated workspace creation.
	select {
	case winner := <-done:
		t.Fatalf("dispatcher %d also finished before release; both passed the claim", winner)
	default:
	}
	if ws.calls.Load() != 1 {
		t.Fatalf("workspace creations = %d, want 1", ws.calls.Load())
	}
	if got := store.deliveries[runClaimKey(task.ID)]; !got {
		t.Fatal("winning dispatch did not hold a store claim during workspace creation")
	}

	close(ws.release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the claim winner did not finish after release")
	}

	active, err := store.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 1 || active[0].TaskID != task.ID {
		t.Fatalf("active runs = %+v, want one run for %s", active, task.ID)
	}
	if len(store.deliveries) != 0 {
		t.Fatalf("claims left in the store: %v", store.deliveries)
	}
}

func TestDispatchFullCapacityReturnsCapFullSkip(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	active := core.NewRun("r-busy", "T-BUSY", time.Now().UTC())
	active.State = core.RunRunning
	active.HoldAgent()
	if err := store.Save(ctx, active); err != nil {
		t.Fatalf("Save(active): %v", err)
	}
	tasks := []core.Task{
		{ID: "T-A", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("a.go")}},
		{ID: "T-B", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("b.go")}},
	}
	ws := &fakeWorkspace{}
	d := claimTestDispatcher(store, ws, tasks[0])
	d.Source = &fakeSource{tasks: tasks}

	res, err := d.Dispatch(ctx, 1)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(res.Runs) != 0 || ws.creates != 0 {
		t.Fatalf("runs = %d, creates = %d, want none at capacity", len(res.Runs), ws.creates)
	}
	if len(res.Skipped) != len(tasks) {
		t.Fatalf("skipped = %+v, want one skip per candidate", res.Skipped)
	}
	seen := map[core.TaskID]bool{}
	for _, s := range res.Skipped {
		if seen[s.TaskID] {
			t.Fatalf("two outcomes for %s: %+v", s.TaskID, res.Skipped)
		}
		seen[s.TaskID] = true
		if s.Code != skipCapFullCode {
			t.Errorf("code = %q, want %q", s.Code, skipCapFullCode)
		}
		if !strings.Contains(s.Reason, "requested 1") || !strings.Contains(s.Reason, "1 active runs") {
			t.Errorf("reason = %q, want requested and active counts", s.Reason)
		}
		if s.Next == "" {
			t.Errorf("skip for %s has no next action", s.TaskID)
		}
	}
	for _, task := range tasks {
		if !seen[task.ID] {
			t.Fatalf("%s has no outcome: %+v", task.ID, res.Skipped)
		}
	}
}

func TestDispatchAccountsForEveryCandidateBelowLimit(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	tasks := []core.Task{
		{ID: "T-A", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("a.go")}},
		{ID: "T-B", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("b.go")}},
		{ID: "T-C", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("c.go")}},
	}
	d := claimTestDispatcher(store, &fakeWorkspace{}, tasks[0])
	d.Source = &fakeSource{tasks: tasks}
	d.Concurrency = core.Concurrency{MaxInFlight: 3, MaxActiveAgents: 1}

	res, err := d.Dispatch(ctx, 1)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(res.Runs) != 1 || len(res.Skipped) != 2 {
		t.Fatalf("runs = %d, skipped = %+v, want one run and two skips", len(res.Runs), res.Skipped)
	}
	seen := map[core.TaskID]bool{}
	for _, run := range res.Runs {
		seen[run.TaskID] = true
	}
	for _, skip := range res.Skipped {
		if seen[skip.TaskID] {
			t.Fatalf("%s is both a run and a skip", skip.TaskID)
		}
		if skip.Code != skipCapFullCode {
			t.Errorf("skip for %s has code %q, want %q", skip.TaskID, skip.Code, skipCapFullCode)
		}
		seen[skip.TaskID] = true
	}
	if len(seen) != len(tasks) {
		t.Fatalf("outcomes = %v, want one per candidate", seen)
	}
}

func TestDispatchReleasesClaimAfterFailedStart(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	task := core.Task{
		ID: "T-RETRY", Status: core.TaskAvailable,
		Scope: []core.ScopePath{core.ScopeFile("retry.go")},
	}
	ws := &fakeWorkspace{createErrOnce: errors.New("workspace create failed")}
	d := claimTestDispatcher(store, ws, task)

	res, err := d.Dispatch(ctx, 1)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(res.Failures) != 1 {
		t.Fatalf("failures = %+v, want one", res.Failures)
	}
	if len(store.deliveries) != 0 {
		t.Fatalf("claim held after a failed start: %v", store.deliveries)
	}

	d = claimTestDispatcher(store, &fakeWorkspace{}, task)
	res, err = d.Dispatch(ctx, 1)
	if err != nil {
		t.Fatalf("second Dispatch: %v", err)
	}
	if len(res.Runs) != 1 {
		t.Fatalf("second Dispatch runs = %d, want 1", len(res.Runs))
	}
	if len(store.deliveries) != 0 {
		t.Fatalf("claim held after a successful start: %v", store.deliveries)
	}
}

func TestStartOneHonoursStoreClaim(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	task := core.Task{
		ID: "T-START", Status: core.TaskAvailable,
		Scope: []core.ScopePath{core.ScopeFile("start.go")},
	}
	if _, err := store.ClaimDelivery(ctx, runClaimKey(task.ID)); err != nil {
		t.Fatalf("ClaimDelivery: %v", err)
	}
	d := claimTestDispatcher(store, &fakeWorkspace{}, task)

	out, err := d.StartOne(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartOne: %v", err)
	}
	if out.RunID != "" || out.Code != string(SkipActiveRun) {
		t.Fatalf("outcome = %+v, want a refusal with no run", out)
	}
	if len(store.runs) != 0 {
		t.Fatalf("a refused start saved %d runs", len(store.runs))
	}
	if err := store.ReleaseDelivery(ctx, runClaimKey(task.ID)); err != nil {
		t.Fatalf("ReleaseDelivery: %v", err)
	}
	out, err = d.StartOne(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartOne after release: %v", err)
	}
	if out.RunID == "" {
		t.Fatalf("outcome = %+v, want a started run", out)
	}
	if len(store.deliveries) != 0 {
		t.Fatalf("claim held after StartOne: %v", store.deliveries)
	}
}

package app

import (
	"context"
	"fmt"
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
			return
		}
	}
	t.Fatal("active task was not reported as skipped")
}

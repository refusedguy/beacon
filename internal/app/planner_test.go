package app

import (
	"fmt"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestPlannerSelectsTwentyNonOverlappingTasks(t *testing.T) {
	var tasks []core.Task
	for i := 0; i < 20; i++ {
		tasks = append(tasks, core.Task{
			ID:     core.TaskID(fmt.Sprintf("T-%02d", i)),
			Title:  "task",
			Status: core.TaskAvailable,
			Scope:  []core.ScopePath{core.ScopeFile(fmt.Sprintf("internal/mod%d/file.go", i))},
		})
	}
	chosen, skipped, err := NewPlanner(nil).Select(tasks, nil, 20, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(chosen) != 20 || len(skipped) != 0 {
		t.Fatalf("selected %d, skipped %d", len(chosen), len(skipped))
	}
	seen := map[core.TaskID]bool{}
	for _, c := range chosen {
		seen[c.ID] = true
	}
	if len(seen) != 20 {
		t.Fatal("duplicate task selected")
	}
}

func TestPlannerSkipsOverlappingAndBusyScopes(t *testing.T) {
	tasks := []core.Task{
		{ID: "A", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("a.go")}},
		{ID: "B", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("a.go")}},
		{ID: "E", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("e.go")}},
	}
	active := []*core.Run{{ID: "r1", TaskID: "Z", State: core.RunRunning, Scope: []core.ScopePath{core.ScopeFile("e.go")}}}
	chosen, skipped, err := NewPlanner(nil).Select(tasks, active, 5, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(chosen) != 1 || chosen[0].ID != "A" {
		t.Fatalf("unexpected selection: %+v", chosen)
	}
	if len(skipped) != 2 {
		t.Fatalf("expected 2 skipped, got %+v", skipped)
	}
}

func TestPlannerSerializesTasksWithoutScope(t *testing.T) {
	tasks := []core.Task{
		{ID: "A", Status: core.TaskAvailable},
		{ID: "B", Status: core.TaskAvailable},
	}
	p := NewPlanner(nil)
	chosen, _, err := p.Select(tasks, nil, 2, false)
	if err != nil || len(chosen) != 1 {
		t.Fatalf("chosen=%d err=%v", len(chosen), err)
	}
	active := []*core.Run{{ID: "r1", TaskID: chosen[0].ID, State: core.RunRunning, Scope: chosen[0].Scope}}
	chosen2, _, err := p.Select(tasks, active, 2, false)
	if err != nil || len(chosen2) != 0 {
		t.Fatalf("unscoped tasks must not run beside an active unscoped task: %+v %v", chosen2, err)
	}
	_, skipped, err := p.Select(tasks, nil, 2, true)
	if err != nil || len(skipped) != 2 {
		t.Fatalf("strict mode should skip unscoped tasks: %+v %v", skipped, err)
	}
}

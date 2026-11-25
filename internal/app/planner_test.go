package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func equalPaths(a, b []core.ScopePath) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

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
		t.Fatalf(`NewPlanner(nil).Select(tasks, nil, 20, false): %v`, err)
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
		t.Fatalf(`NewPlanner(nil).Select(tasks, active, 5, false): %v`, err)
	}
	if len(chosen) != 1 || chosen[0].ID != "A" {
		t.Fatalf("unexpected selection: %+v", chosen)
	}
	byID := map[core.TaskID]SkippedTask{}
	for _, s := range skipped {
		byID[s.TaskID] = s
	}
	if len(byID) != 2 {
		t.Fatalf("expected 2 skipped, got %+v", skipped)
	}

	activeSkip, ok := byID["E"]
	if !ok {
		t.Fatalf("active conflict not reported: %+v", skipped)
	}
	if activeSkip.Code != SkipActiveScopeOverlap {
		t.Errorf("active conflict code = %q, want %q", activeSkip.Code, SkipActiveScopeOverlap)
	}
	if activeSkip.BlockedBy != "Z" {
		t.Errorf("active conflict blocked_by = %q, want Z", activeSkip.BlockedBy)
	}
	if activeSkip.BlockedByRun != "r1" {
		t.Errorf("active conflict blocked_by_run = %q, want r1", activeSkip.BlockedByRun)
	}
	if !equalPaths(activeSkip.Overlap, []core.ScopePath{core.ScopeFile("e.go")}) {
		t.Errorf("active conflict overlap = %+v", activeSkip.Overlap)
	}
	if activeSkip.Reason != "scope overlaps Z on f:e.go" {
		t.Errorf("active conflict reason = %q", activeSkip.Reason)
	}

	selectedSkip, ok := byID["B"]
	if !ok {
		t.Fatalf("selected conflict not reported: %+v", skipped)
	}
	if selectedSkip.Code != SkipSelectedScopeOverlap {
		t.Errorf("selected conflict code = %q, want %q", selectedSkip.Code, SkipSelectedScopeOverlap)
	}
	if selectedSkip.BlockedBy != "A" {
		t.Errorf("selected conflict blocked_by = %q, want A", selectedSkip.BlockedBy)
	}
	if selectedSkip.BlockedByRun != "" {
		t.Errorf("selected conflict blocked_by_run = %q, want empty", selectedSkip.BlockedByRun)
	}
	if !equalPaths(selectedSkip.Overlap, []core.ScopePath{core.ScopeFile("a.go")}) {
		t.Errorf("selected conflict overlap = %+v", selectedSkip.Overlap)
	}
	if selectedSkip.Reason != "scope overlaps A on f:a.go" {
		t.Errorf("selected conflict reason = %q", selectedSkip.Reason)
	}
}

func TestPlannerReportsStatusScopeAndActiveRunCodes(t *testing.T) {
	done := core.Task{ID: "D", Status: core.TaskDone, Scope: []core.ScopePath{core.ScopeFile("d.go")}}
	unscoped := core.Task{ID: "U", Status: core.TaskAvailable}
	busy := core.Task{ID: "R", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("r.go")}}
	active := []*core.Run{{ID: "r-run", TaskID: "R", State: core.RunRunning, Scope: busy.Scope}}

	_, skipped, err := NewPlanner(nil).Select([]core.Task{done, unscoped, busy}, active, 5, true)
	if err != nil {
		t.Fatalf(`NewPlanner(nil).Select([]core.Task{done, unscoped, busy}, active, 5, true): %v`, err)
	}
	byID := map[core.TaskID]SkippedTask{}
	for _, s := range skipped {
		byID[s.TaskID] = s
	}
	if len(byID) != 3 {
		t.Fatalf("expected 3 skipped, got %+v", skipped)
	}
	if byID["D"].Code != SkipBadStatus {
		t.Errorf("status skip code = %q, want %q", byID["D"].Code, SkipBadStatus)
	}
	if byID["U"].Code != SkipNoScope {
		t.Errorf("no-scope skip code = %q, want %q", byID["U"].Code, SkipNoScope)
	}
	if byID["R"].Code != SkipActiveRun {
		t.Errorf("active-run skip code = %q, want %q", byID["R"].Code, SkipActiveRun)
	}
	if byID["R"].BlockedBy != "R" || byID["R"].BlockedByRun != "r-run" {
		t.Errorf("active-run skip = %+v", byID["R"])
	}
}

func TestSkippedTaskJSONExposesStructuredFields(t *testing.T) {
	s := SkippedTask{
		TaskID:       "T-1",
		Code:         SkipActiveScopeOverlap,
		Reason:       "scope overlaps T-2 on f:server.go",
		BlockedBy:    "T-2",
		BlockedByRun: "r1",
		Overlap:      []core.ScopePath{core.ScopeFile("server.go")},
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf(`json.Marshal(s): %v`, err)
	}
	got := string(data)
	for _, want := range []string{
		`"task_id":"T-1"`,
		`"code":"active_scope_overlap"`,
		`"reason":"scope overlaps T-2 on f:server.go"`,
		`"blocked_by":"T-2"`,
		`"blocked_by_run":"r1"`,
		`"overlap":["f:server.go"]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON %s is missing %s", got, want)
		}
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

func TestPlannerUsesSharedOverlapAuthority(t *testing.T) {
	tasks := []core.Task{
		{ID: "dir", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeDir("src")}},
		{ID: "glob", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopePattern("src/**/*.go")}},
		{ID: "nested", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopePattern("src/lib/*.go")}},
		{ID: "docs", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("docs/readme.md")}},
	}
	for name, p := range map[string]ConflictPlanner{
		"default checker":     NewPlanner(nil),
		"shared core checker": NewPlanner(core.PathsAndDependencies),
	} {
		t.Run(name, func(t *testing.T) {
			chosen, skipped, err := p.Select(tasks, nil, 4, false)
			if err != nil {
				t.Fatalf(`p.Select(tasks, nil, 4, false): %v`, err)
			}
			ids := taskIDs(chosen)
			if len(ids) != 2 || ids[0] != "dir" || ids[1] != "docs" {
				t.Fatalf("chosen = %v, want [dir docs]", ids)
			}
			byID := map[core.TaskID]SkippedTask{}
			for _, s := range skipped {
				byID[s.TaskID] = s
			}
			if len(byID) != 2 {
				t.Fatalf("expected 2 skipped, got %+v", skipped)
			}
			if byID["glob"].Code != SkipSelectedScopeOverlap {
				t.Errorf("glob skip = %+v, want selected scope overlap", byID["glob"])
			}
			if byID["nested"].Code != SkipSelectedScopeOverlap {
				t.Errorf("nested skip = %+v, want selected scope overlap", byID["nested"])
			}
		})
	}
}

func TestActiveConflictDoesNotMatchCardNamedLikeSyntheticID(t *testing.T) {
	active := []*core.Run{{
		ID: "r1", TaskID: "run-card", State: core.RunRunning,
		Scope: []core.ScopePath{core.ScopeFile("active.go")},
	}}
	tasks := []core.Task{
		{ID: "__active__", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("free.go")}},
		{ID: "clash", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("active.go")}},
	}
	// This checker matches by ID as well as by scope, so a synthetic card
	// named __active__ would block the real card with that ID.
	chosen, skipped, err := NewPlanner(core.Task.Overlaps).Select(tasks, active, 2, false)
	if err != nil {
		t.Fatalf(`Select(tasks, active, 2, false): %v`, err)
	}
	if ids := taskIDs(chosen); len(ids) != 1 || ids[0] != "__active__" {
		t.Fatalf("chosen = %v, want [__active__]", ids)
	}
	byID := map[core.TaskID]SkippedTask{}
	for _, s := range skipped {
		byID[s.TaskID] = s
	}
	if len(byID) != 1 {
		t.Fatalf("expected 1 skipped, got %+v", skipped)
	}
	if byID["clash"].Code != SkipActiveScopeOverlap {
		t.Errorf("clash skip = %+v, want active scope overlap", byID["clash"])
	}
}

func TestActiveConflictDoesNotMatchSyntheticIDAsDependency(t *testing.T) {
	active := []*core.Run{{
		ID: "r1", TaskID: "run-card", State: core.RunRunning,
		Scope: []core.ScopePath{core.ScopeFile("active.go")},
	}}
	tasks := []core.Task{{
		ID: "dependent", Status: core.TaskAvailable,
		DependsOn: []core.TaskID{"__active__"},
		Scope:     []core.ScopePath{core.ScopeFile("free.go")},
	}}
	chosen, skipped, err := NewPlanner(nil).Select(tasks, active, 1, false)
	if err != nil {
		t.Fatalf(`Select(tasks, active, 1, false): %v`, err)
	}
	if len(chosen) != 1 || len(skipped) != 0 {
		t.Fatalf("chosen = %v, skipped = %+v", chosen, skipped)
	}
}

func taskIDs(tasks []core.Task) []core.TaskID {
	ids := make([]core.TaskID, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

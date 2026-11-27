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

func TestPlannerInjectedCheckerDecidesActiveScopeConflicts(t *testing.T) {
	// Both live runs claim paths under src, so each path-overlaps the candidate
	// and reaches the injected checker through the index. The caller's verdict,
	// not the path comparison, must decide which run blocks the candidate.
	candidate := core.Task{
		ID: "T", Status: core.TaskAvailable,
		Scope: []core.ScopePath{core.ScopeDir("src")},
	}
	active := []*core.Run{
		{ID: "r-allow", TaskID: "A-allow", State: core.RunRunning, Scope: []core.ScopePath{core.ScopeFile("src/allow.go")}},
		{ID: "r-block", TaskID: "A-block", State: core.RunRunning, Scope: []core.ScopePath{core.ScopeFile("src/block.go")}},
	}
	hasScopeValue := func(scope []core.ScopePath, want string) bool {
		for _, p := range scope {
			if p.Value() == want {
				return true
			}
		}
		return false
	}

	for _, tc := range []struct {
		name             string
		blocks           func(core.Task) bool
		wantBlockedBy    core.TaskID
		wantBlockedByRun core.RunID
	}{
		{
			name:             "checker blocks the later run",
			blocks:           func(run core.Task) bool { return hasScopeValue(run.Scope, "src/block.go") },
			wantBlockedBy:    "A-block",
			wantBlockedByRun: "r-block",
		},
		{
			name:   "checker allows both overlapping runs",
			blocks: func(core.Task) bool { return false },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var probes int
			checker := func(a, b core.Task) bool {
				if a.ID == probeCandidateID && b.ID == probeRunID {
					probes++
					return tc.blocks(b)
				}
				t.Fatalf("unexpected overlap check with a=%+v b=%+v", a, b)
				return false
			}
			chosen, skipped, err := NewPlanner(checker).Select([]core.Task{candidate}, active, 1, false)
			if err != nil {
				t.Fatalf(`Select(candidate, active, 1, false): %v`, err)
			}
			if probes != len(active) {
				t.Errorf("active overlap checks = %d, want %d", probes, len(active))
			}
			if tc.wantBlockedByRun == "" {
				if ids := taskIDs(chosen); len(ids) != 1 || ids[0] != "T" {
					t.Fatalf("chosen = %v, want [T]", ids)
				}
				if len(skipped) != 0 {
					t.Fatalf("skipped = %+v, want none", skipped)
				}
				return
			}
			if len(chosen) != 0 {
				t.Fatalf("chosen = %+v, want none", chosen)
			}
			if len(skipped) != 1 {
				t.Fatalf("skipped = %+v, want one active conflict", skipped)
			}
			got := skipped[0]
			if got.TaskID != "T" || got.Code != SkipActiveScopeOverlap {
				t.Errorf("skip = %+v, want active scope overlap for T", got)
			}
			if got.BlockedBy != tc.wantBlockedBy {
				t.Errorf("blocked_by = %q, want %q", got.BlockedBy, tc.wantBlockedBy)
			}
			if got.BlockedByRun != tc.wantBlockedByRun {
				t.Errorf("blocked_by_run = %q, want %q", got.BlockedByRun, tc.wantBlockedByRun)
			}
		})
	}
}

func taskIDs(tasks []core.Task) []core.TaskID {
	ids := make([]core.TaskID, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

func largeDispatchFixtures() ([]core.Task, []*core.Run) {
	const (
		candidateCount = 1000
		activeCount    = 500
		pathCount      = 3
	)
	tasks := make([]core.Task, candidateCount)
	for i := range tasks {
		scope := make([]core.ScopePath, pathCount)
		for j := range scope {
			scope[j] = core.ScopeFile(fmt.Sprintf("internal/task%04d/file%d.go", i, j))
		}
		if i < activeCount {
			scope[0] = core.ScopeFile(fmt.Sprintf("internal/active%04d/file0.go", i))
		}
		tasks[i] = core.Task{
			ID:     core.TaskID(fmt.Sprintf("T-%04d", i)),
			Status: core.TaskAvailable,
			Scope:  scope,
		}
	}
	active := make([]*core.Run, activeCount)
	for i := range active {
		scope := make([]core.ScopePath, pathCount)
		for j := range scope {
			scope[j] = core.ScopeFile(fmt.Sprintf("internal/active%04d/file%d.go", i, j))
		}
		active[i] = &core.Run{
			ID:     core.RunID(fmt.Sprintf("run-%04d", i)),
			TaskID: core.TaskID(fmt.Sprintf("A-%04d", i)),
			State:  core.RunRunning,
			Scope:  scope,
		}
	}
	return tasks, active
}

// referenceSelect repeats the straightforward scan the index replaces. It is
// the oracle for the large dispatch case, not a production path.
func referenceSelect(candidates []core.Task, active []*core.Run, limit int) []core.Task {
	var selected []core.Task
	for _, t := range candidates {
		if len(selected) >= limit {
			break
		}
		if len(t.Scope) == 0 {
			t.Scope = []core.ScopePath{core.ScopeAll()}
		}
		conflict := false
		for _, r := range active {
			if r == nil || r.State.Terminal() {
				continue
			}
			if core.OverlapByPaths(t, core.Task{Scope: r.Scope}) {
				conflict = true
				break
			}
		}
		if conflict {
			continue
		}
		for _, s := range selected {
			if core.PathsAndDependencies(t, s) {
				conflict = true
				break
			}
		}
		if conflict {
			continue
		}
		selected = append(selected, t)
	}
	return selected
}

// referencePlan mirrors the full-scan planner. It is the oracle for behavior
// preservation, independent of the active-scope index.
func referencePlan(candidates []core.Task, active []*core.Run, limit int, strict bool) ([]core.Task, []SkippedTask) {
	var selected []core.Task
	var skipped []SkippedTask
	if limit <= 0 {
		return selected, skipped
	}
	busy := make(map[core.TaskID]*core.Run)
	var live []*core.Run
	for _, r := range active {
		if r == nil || r.State.Terminal() {
			continue
		}
		busy[r.TaskID] = r
		live = append(live, r)
	}
	for _, t := range candidates {
		if len(selected) >= limit {
			break
		}
		if t.Status != core.TaskAvailable && t.Status != "" {
			skipped = append(skipped, SkippedTask{TaskID: t.ID, Code: SkipBadStatus, Reason: "task status is " + string(t.Status)})
			continue
		}
		if r, ok := busy[t.ID]; ok {
			skipped = append(skipped, SkippedTask{
				TaskID: t.ID, Code: SkipActiveRun, BlockedBy: t.ID, BlockedByRun: r.ID,
				Reason: "task already has an active run",
			})
			continue
		}
		if len(t.Scope) == 0 {
			if strict {
				skipped = append(skipped, SkippedTask{
					TaskID: t.ID, Code: SkipNoScope, Reason: "task has no scope and strict_scope is enabled",
				})
				continue
			}
			t.Scope = []core.ScopePath{core.ScopeAll()}
		}
		blocked := false
		for _, r := range live {
			if !core.OverlapByPaths(t, core.Task{Scope: r.Scope}) {
				continue
			}
			paths := intersectScopes(t.Scope, r.Scope)
			skipped = append(skipped, SkippedTask{
				TaskID: t.ID, Code: SkipActiveScopeOverlap, BlockedBy: r.TaskID, BlockedByRun: r.ID,
				Overlap: paths, Reason: "scope overlaps " + string(r.TaskID) + overlapSuffix(paths),
			})
			blocked = true
			break
		}
		if blocked {
			continue
		}
		for _, s := range selected {
			if !core.PathsAndDependencies(t, s) {
				continue
			}
			paths := intersectScopes(t.Scope, s.Scope)
			skipped = append(skipped, SkippedTask{
				TaskID: t.ID, Code: SkipSelectedScopeOverlap, BlockedBy: s.ID,
				Overlap: paths, Reason: "scope overlaps " + string(s.ID) + overlapSuffix(paths),
			})
			blocked = true
			break
		}
		if blocked {
			continue
		}
		selected = append(selected, t)
	}
	return selected, skipped
}

func TestPlannerIndexPreservesFullScanBehavior(t *testing.T) {
	candidates := []core.Task{
		{ID: "file", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("src/a.go")}},
		{ID: "dir", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeDir("src")}},
		{ID: "glob", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopePattern("src/**/*.go")}},
		{ID: "top-glob", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopePattern("*.go")}},
		{ID: "leading-glob", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopePattern("*/*.go")}},
		{ID: "deep-glob", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopePattern("a/**/z.go")}},
		{ID: "question-glob", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopePattern("src/f?.go")}},
		{ID: "all", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeAll()}},
		{ID: "unscoped", Status: core.TaskAvailable},
		{ID: "depend", Status: core.TaskAvailable, DependsOn: []core.TaskID{"file"}, Scope: []core.ScopePath{core.ScopeFile("other.go")}},
		{ID: "done", Status: core.TaskDone, Scope: []core.ScopePath{core.ScopeFile("done.go")}},
	}
	active := []*core.Run{
		{ID: "r-file", TaskID: "A-file", State: core.RunRunning, Scope: []core.ScopePath{core.ScopeFile("src/a.go")}},
		{ID: "r-dir", TaskID: "A-dir", State: core.RunRunning, Scope: []core.ScopePath{core.ScopeDir("lib")}},
		{ID: "r-glob", TaskID: "A-glob", State: core.RunRunning, Scope: []core.ScopePath{core.ScopePattern("**/*.md")}},
		{ID: "r-top-glob", TaskID: "A-top-glob", State: core.RunRunning, Scope: []core.ScopePath{core.ScopePattern("*.go")}},
		{ID: "r-top-file", TaskID: "A-top-file", State: core.RunRunning, Scope: []core.ScopePath{core.ScopeFile("top.go")}},
		{ID: "r-deep", TaskID: "A-deep", State: core.RunRunning, Scope: []core.ScopePath{core.ScopeFile("a/b/c/z.go")}},
		{ID: "r-done", TaskID: "A-done", State: core.RunMerged, Scope: []core.ScopePath{core.ScopeFile("done.go")}},
		nil,
	}
	for _, strict := range []bool{false, true} {
		for _, limit := range []int{0, 1, 3, 100} {
			got, skipped, err := NewPlanner(nil).Select(candidates, active, limit, strict)
			if err != nil {
				t.Fatal(err)
			}
			wantTasks, wantSkipped := referencePlan(candidates, active, limit, strict)
			if gotIDs, wantIDs := taskIDs(got), taskIDs(wantTasks); len(gotIDs) != len(wantIDs) {
				t.Fatalf("strict=%v limit=%d selected %v, want %v", strict, limit, gotIDs, wantIDs)
			} else {
				for i := range gotIDs {
					if gotIDs[i] != wantIDs[i] {
						t.Fatalf("strict=%v limit=%d selected %v, want %v", strict, limit, gotIDs, wantIDs)
					}
				}
			}
			gotJSON, err := json.Marshal(skipped)
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, err := json.Marshal(wantSkipped)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("strict=%v limit=%d skipped = %s, want %s", strict, limit, gotJSON, wantJSON)
			}
		}
	}
}

func TestPlannerIndexBoundsActiveChecks(t *testing.T) {
	tasks, active := largeDispatchFixtures()
	const limit = 20
	var activeChecks, selectedChecks int
	checker := func(a, b core.Task) bool {
		if a.ID == probeCandidateID && b.ID == probeRunID {
			activeChecks++
		} else {
			selectedChecks++
		}
		return core.PathsAndDependencies(a, b)
	}
	chosen, _, err := NewPlanner(checker).Select(tasks, active, limit, false)
	if err != nil {
		t.Fatalf(`Select(tasks, active, %d, false): %v`, limit, err)
	}
	want := referenceSelect(tasks, active, limit)
	if gotIDs, wantIDs := taskIDs(chosen), taskIDs(want); len(gotIDs) != len(wantIDs) {
		t.Fatalf("selected %v, want %v", gotIDs, wantIDs)
	} else {
		for i := range gotIDs {
			if gotIDs[i] != wantIDs[i] {
				t.Fatalf("selected %v, want %v", gotIDs, wantIDs)
			}
		}
	}
	// Only 500 candidates claim an active path, so at most 500 of the active
	// runs can be confirmed. A full scan would perform up to 1000*500 checks.
	if activeChecks > len(tasks)/2 {
		t.Fatalf("active overlap checks = %d, want at most %d", activeChecks, len(tasks)/2)
	}
	// The remaining checks are the candidate-to-selected comparisons, which
	// are bounded by the dispatch limit.
	if selectedChecks > limit*len(tasks) {
		t.Fatalf("selected overlap checks = %d, want at most %d", selectedChecks, limit*len(tasks))
	}
}

func TestClaimDirKeys(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scope core.ScopePath
		want  []core.ScopePath
	}{
		{"single file", core.ScopeFile("one.go"), []core.ScopePath{core.ScopeDir("one.go")}},
		{"nested path", core.ScopeFile("a/b/c.go"), []core.ScopePath{core.ScopeDir("a"), core.ScopeDir("a/b"), core.ScopeDir("a/b/c.go")}},
		{"leading wildcard", core.ScopePattern("*/b.go"), nil},
		{"inner wildcard", core.ScopePattern("a/*/c.go"), []core.ScopePath{core.ScopeDir("a")}},
		{"trailing slash", core.ScopeDir("a/b/"), []core.ScopePath{core.ScopeDir("a"), core.ScopeDir("a/b")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := claimDirKeys(nil, tc.scope); !equalPaths(got, tc.want) {
				t.Fatalf("claimDirKeys(%q) = %v, want %v", tc.scope, got, tc.want)
			}
		})
	}
}

func TestClaimDirKeysReusesBufferWithoutChangingKeys(t *testing.T) {
	scopes := []core.ScopePath{
		core.ScopeFile("a/b/c.go"),
		core.ScopePattern("*/b.go"),
		core.ScopePattern("a/*/c.go"),
		core.ScopeDir("x/y/"),
		core.ScopeFile("one.go"),
	}
	var buf []core.ScopePath
	for _, scope := range scopes {
		got := claimDirKeys(buf, scope)
		buf = got
		want := claimDirKeys(nil, scope)
		if !equalPaths(got, want) {
			t.Fatalf("claimDirKeys with reused buffer for %q = %v, want %v", scope, got, want)
		}
	}
}

func TestLeadingWildcard(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"*.go", true},
		{"?*.go", true},
		{"*/*.go", true},
		{"src/*.go", false},
		{"src/a/*/c.go", false},
		{"src/*.go/lead", false},
		{"plain.go", false},
		{"", false},
		{"a/b?/c", false},
		{"a/*", false},
	} {
		if got := leadingWildcard(tc.value); got != tc.want {
			t.Errorf("leadingWildcard(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestActiveScopeIndexMatchingStableAcrossCalls(t *testing.T) {
	active := []*core.Run{
		{ID: "r1", TaskID: "A1", State: core.RunRunning, Scope: []core.ScopePath{core.ScopeFile("src/a.go")}},
		{ID: "r2", TaskID: "A2", State: core.RunRunning, Scope: []core.ScopePath{core.ScopeFile("src/b.go")}},
		{ID: "r3", TaskID: "A3", State: core.RunRunning, Scope: []core.ScopePath{core.ScopeFile("docs/readme.md")}},
	}
	idx := newActiveScopeIndex(active)
	tasks := []core.Task{
		{ID: "t1", Scope: []core.ScopePath{core.ScopeFile("src/a.go")}},
		{ID: "t2", Scope: []core.ScopePath{core.ScopeFile("src/a.go"), core.ScopeFile("src/b.go")}},
		{ID: "t3", Scope: []core.ScopePath{core.ScopeDir("src")}},
		{ID: "t4", Scope: []core.ScopePath{core.ScopeAll()}},
		{ID: "t5", Scope: []core.ScopePath{core.ScopePattern("*.go")}},
	}
	for pass := 0; pass < 3; pass++ {
		for _, task := range tasks {
			got := idx.matching(task)
			want := newActiveScopeIndex(active).matching(task)
			if !equalInts(got, want) {
				t.Fatalf("pass %d task %s matching = %v, want %v", pass, task.ID, got, want)
			}
		}
	}
}

func equalInts(a, b []int) bool {
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

func BenchmarkPlannerSelectLargeDispatch(b *testing.B) {
	tasks, active := largeDispatchFixtures()
	const limit = 20
	var checks int
	checker := func(a, bb core.Task) bool {
		checks++
		return core.PathsAndDependencies(a, bb)
	}
	p := NewPlanner(checker)
	chosen, _, err := p.Select(tasks, active, limit, false)
	if err != nil || len(chosen) != limit || checks > 2*limit*len(tasks) {
		b.Fatalf("chosen=%d checks=%d err=%v", len(chosen), checks, err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := p.Select(tasks, active, limit, false)
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(checks)/float64(b.N), "overlap-checks/op")
}

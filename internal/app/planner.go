package app

import (
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
)

// SkipCode is a stable reason a task was left out of a dispatch.
type SkipCode string

const (
	SkipActiveRun            SkipCode = "active_run"
	SkipActiveScopeOverlap   SkipCode = "active_scope_overlap"
	SkipSelectedScopeOverlap SkipCode = "selected_scope_overlap"
	SkipNoScope              SkipCode = "no_scope"
	SkipBadStatus            SkipCode = "bad_status"
)

// SkippedTask explains why a candidate was not dispatched. Overlap holds the
// intersecting paths from the skipped task's own scope.
type SkippedTask struct {
	TaskID       core.TaskID      `json:"task_id"`
	Code         SkipCode         `json:"code"`
	Reason       string           `json:"reason"`
	BlockedBy    core.TaskID      `json:"blocked_by,omitempty"`
	BlockedByRun core.RunID       `json:"blocked_by_run,omitempty"`
	Overlap      []core.ScopePath `json:"overlap,omitempty"`
}

type ConflictPlanner interface {
	Select(candidates []core.Task, active []*core.Run, limit int, strict bool) ([]core.Task, []SkippedTask, error)
}

type GreedyPlanner struct {
	Overlap core.OverlapChecker
}

var _ func(core.OverlapChecker) ConflictPlanner = NewPlanner

func NewPlanner(checker core.OverlapChecker) ConflictPlanner {
	if checker == nil {
		checker = core.PathsAndDependencies
	}
	return GreedyPlanner{Overlap: checker}
}

func (p GreedyPlanner) Select(candidates []core.Task, active []*core.Run, limit int, strict bool) ([]core.Task, []SkippedTask, error) {
	var selected []core.Task
	var skipped []SkippedTask
	if limit <= 0 {
		return selected, skipped, nil
	}
	busyIDs := make(map[core.TaskID]*core.Run)
	var live []*core.Run
	for _, r := range active {
		if r == nil || r.State.Terminal() {
			continue
		}
		busyIDs[r.TaskID] = r
		live = append(live, r)
	}

	for _, t := range candidates {
		if len(selected) >= limit {
			break
		}
		if t.Status != core.TaskAvailable && t.Status != "" {
			skipped = append(skipped, SkippedTask{
				TaskID: t.ID,
				Code:   SkipBadStatus,
				Reason: "task status is " + string(t.Status),
			})
			continue
		}
		if activeRun, ok := busyIDs[t.ID]; ok {
			skipped = append(skipped, SkippedTask{
				TaskID:       t.ID,
				Code:         SkipActiveRun,
				BlockedBy:    t.ID,
				BlockedByRun: activeRun.ID,
				Reason:       "task already has an active run",
			})
			continue
		}
		if len(t.Scope) == 0 {
			if strict {
				skipped = append(skipped, SkippedTask{
					TaskID: t.ID,
					Code:   SkipNoScope,
					Reason: "task has no scope and strict_scope is enabled",
				})
				continue
			}
			t.Scope = []core.ScopePath{core.ScopeAll()}
		}
		if blocker, paths := p.activeConflict(t, live); blocker != nil {
			skipped = append(skipped, SkippedTask{
				TaskID:       t.ID,
				Code:         SkipActiveScopeOverlap,
				BlockedBy:    blocker.TaskID,
				BlockedByRun: blocker.ID,
				Overlap:      paths,
				Reason:       "scope overlaps " + string(blocker.TaskID) + overlapSuffix(paths),
			})
			continue
		}
		conflict := false
		for _, s := range selected {
			if p.Overlap(t, s) {
				paths := intersectScopes(t.Scope, s.Scope)
				skipped = append(skipped, SkippedTask{
					TaskID:    t.ID,
					Code:      SkipSelectedScopeOverlap,
					BlockedBy: s.ID,
					Overlap:   paths,
					Reason:    "scope overlaps " + string(s.ID) + overlapSuffix(paths),
				})
				conflict = true
				break
			}
		}
		if conflict {
			continue
		}
		selected = append(selected, t)
	}
	return selected, skipped, nil
}

// activeConflict returns the first active run whose scope overlaps t and the
// paths from t that intersect that run.
func (p GreedyPlanner) activeConflict(t core.Task, active []*core.Run) (*core.Run, []core.ScopePath) {
	for _, r := range active {
		if r == nil || r.State.Terminal() {
			continue
		}
		for _, s := range r.Scope {
			if p.Overlap(t, core.Task{ID: "__active__", Scope: []core.ScopePath{s}}) {
				return r, intersectScopes(t.Scope, r.Scope)
			}
		}
	}
	return nil, nil
}

// intersectScopes returns the entries of a that cover or are covered by an
// entry of b, in the order they appear in a.
func intersectScopes(a, b []core.ScopePath) []core.ScopePath {
	var out []core.ScopePath
	seen := make(map[core.ScopePath]bool)
	for _, x := range a {
		for _, y := range b {
			if x.Covers(y) || y.Covers(x) {
				if !seen[x] {
					seen[x] = true
					out = append(out, x)
				}
				break
			}
		}
	}
	return out
}

func overlapSuffix(paths []core.ScopePath) string {
	if len(paths) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(" on ")
	for i, p := range paths {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(string(p))
	}
	return b.String()
}

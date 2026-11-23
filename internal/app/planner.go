package app

import "github.com/refusedguy/beacon/pkg/core"

type ConflictPlanner interface {
	Select(candidates []core.Task, active []*core.Run, limit int, strict bool) ([]core.Task, []SkippedTask, error)
}

type SkippedTask struct {
	TaskID core.TaskID
	Reason string
}

type GreedyPlanner struct {
	Overlap core.OverlapChecker
}

func NewPlanner(checker core.OverlapChecker) GreedyPlanner {
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
	busyIDs := make(map[core.TaskID]bool)
	var busyScopes []core.ScopePath
	for _, r := range active {
		if r == nil || r.State.Terminal() {
			continue
		}
		busyIDs[r.TaskID] = true
		busyScopes = append(busyScopes, r.Scope...)
	}

	for _, t := range candidates {
		if len(selected) >= limit {
			break
		}
		if t.Status != core.TaskAvailable && t.Status != "" {
			skipped = append(skipped, SkippedTask{t.ID, "task status is " + string(t.Status)})
			continue
		}
		if busyIDs[t.ID] {
			skipped = append(skipped, SkippedTask{t.ID, "task already has an active run"})
			continue
		}
		if len(t.Scope) == 0 {
			if strict {
				skipped = append(skipped, SkippedTask{t.ID, "task has no scope and strict_scope is enabled"})
				continue
			}
			t.Scope = []core.ScopePath{core.ScopeAll()}
		}
		if overlapsAny(t, busyScopes, p.Overlap) {
			skipped = append(skipped, SkippedTask{t.ID, "scope overlaps an active run"})
			continue
		}
		conflict := false
		for _, s := range selected {
			if p.Overlap(t, s) {
				conflict = true
				break
			}
		}
		if conflict {
			skipped = append(skipped, SkippedTask{t.ID, "scope overlaps another selected task"})
			continue
		}
		selected = append(selected, t)
	}
	return selected, skipped, nil
}

func overlapsAny(t core.Task, scopes []core.ScopePath, checker core.OverlapChecker) bool {
	for _, s := range scopes {
		if checker(t, core.Task{ID: "__active__", Scope: []core.ScopePath{s}}) {
			return true
		}
	}
	return false
}

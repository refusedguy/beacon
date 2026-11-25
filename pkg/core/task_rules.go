package core

import "strings"

// NormalizeTaskKind maps a wire value to a task kind. Anything other than an
// epic is a task, so an unset field needs no special case.
func NormalizeTaskKind(kind TaskKind) TaskKind {
	if strings.EqualFold(strings.TrimSpace(string(kind)), string(TaskKindEpic)) {
		return TaskKindEpic
	}
	return TaskKindTask
}

// NormalizeReviewMode maps the accepted review aliases to one value. Create,
// update and card reading all use it, so the same input means the same mode.
func NormalizeReviewMode(mode ReviewMode) ReviewMode {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case string(ReviewModeHuman), "manual":
		return ReviewModeHuman
	case string(ReviewModeNone), "off", "skip":
		return ReviewModeNone
	default:
		return ReviewModeLLM
	}
}

// ExpandChildren numbers an epic's plan into child tasks. childBase is the
// branch the children branch from. A caller with no epic branch passes an
// empty string and keeps the default branch.
func ExpandChildren(epic Task, childBase string) []Task {
	out := make([]Task, 0, len(epic.Plan))
	for i, spec := range epic.Plan {
		out = append(out, Task{
			ID:         ChildTaskID(epic.ID, i+1),
			Title:      strings.TrimSpace(spec.Title),
			Body:       strings.TrimSpace(spec.Body),
			Scope:      spec.Scope,
			DependsOn:  append([]TaskID(nil), spec.Depends...),
			Priority:   spec.Priority,
			Status:     TaskAvailable,
			Kind:       TaskKindTask,
			BaseBranch: childBase,
		})
	}
	return out
}

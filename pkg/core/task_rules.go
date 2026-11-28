package core

import (
	"sort"
	"strings"
)

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

// ValidateTaskGraph reports a self-dependency or dependency cycle in tasks. It
// names one cycle so a caller can point at the offending cards. A dependency on
// an ID outside tasks is allowed because the graph prunes those edges on write.
func ValidateTaskGraph(tasks []Task) error {
	deps := make(map[TaskID][]TaskID, len(tasks))
	order := make([]TaskID, 0, len(tasks))
	for _, task := range tasks {
		order = append(order, task.ID)
		deps[task.ID] = append(deps[task.ID], task.DependsOn...)
	}
	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[TaskID]int, len(tasks))
	stack := make([]TaskID, 0, len(tasks))
	var walk func(TaskID) []TaskID
	walk = func(id TaskID) []TaskID {
		state[id] = visiting
		stack = append(stack, id)
		for _, dep := range deps[id] {
			if _, ok := deps[dep]; !ok {
				continue
			}
			if dep == id {
				return []TaskID{id, id}
			}
			switch state[dep] {
			case visiting:
				start := 0
				for i, node := range stack {
					if node == dep {
						start = i
						break
					}
				}
				cycle := append([]TaskID(nil), stack[start:]...)
				return append(cycle, dep)
			case unvisited:
				if cycle := walk(dep); len(cycle) > 0 {
					return cycle
				}
			}
		}
		state[id] = visited
		stack = stack[:len(stack)-1]
		return nil
	}
	for _, id := range order {
		if state[id] != unvisited {
			continue
		}
		if cycle := walk(id); len(cycle) > 0 {
			names := make([]string, len(cycle))
			for i, node := range cycle {
				names[i] = string(node)
			}
			return Errorf(CodeInvalidInput, "dependency cycle: %s", strings.Join(names, " -> "))
		}
	}
	return nil
}

// ChildKey canonicalizes a plan's children for identity checks. It sorts by
// title so plan order does not change the key, and includes each child's
// dependency list so editing a dependency changes the key.
func ChildKey(specs []ChildSpec) string {
	entries := make([]childEntry, 0, len(specs))
	for _, spec := range specs {
		deps := make([]string, len(spec.Depends))
		for i, dep := range spec.Depends {
			deps[i] = string(dep)
		}
		entries = append(entries, childEntry{title: strings.TrimSpace(spec.Title), deps: deps})
	}
	return joinChildEntries(entries)
}

// ChildTaskKey is ChildKey for a set of expanded child tasks. Callers use it to
// compare a stored epic's plan with children rebuilt from the same plan.
func ChildTaskKey(tasks []Task) string {
	entries := make([]childEntry, 0, len(tasks))
	for _, task := range tasks {
		deps := make([]string, len(task.DependsOn))
		for i, dep := range task.DependsOn {
			deps[i] = string(dep)
		}
		entries = append(entries, childEntry{title: strings.TrimSpace(task.Title), deps: deps})
	}
	return joinChildEntries(entries)
}

type childEntry struct {
	title string
	deps  []string
}

func joinChildEntries(entries []childEntry) string {
	sort.Slice(entries, func(i, j int) bool { return entries[i].title < entries[j].title })
	var b strings.Builder
	for _, entry := range entries {
		b.WriteString(entry.title)
		b.WriteByte('\x00')
		for _, dep := range entry.deps {
			b.WriteString(dep)
			b.WriteByte('\x1f')
		}
		b.WriteByte('\x00')
	}
	return b.String()
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

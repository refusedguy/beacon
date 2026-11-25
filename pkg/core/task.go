// Package core holds the public contracts of Beacon. It imports only stdlib.
package core

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

type TaskID string

// ScopePath describes a file, a directory subtree, a glob or all files.
type ScopePath string

func ScopeFile(p string) ScopePath { return ScopePath("f:" + cleanPath(p)) }
func ScopeDir(p string) ScopePath  { return ScopePath("d:" + cleanPath(p)) }
func ScopePattern(p string) ScopePath {
	if strings.Contains(p, "**") || strings.Contains(p, "*") {
		return ScopePath("g:" + cleanPath(p))
	}
	return ScopeFile(p)
}
func ScopeAll() ScopePath { return ScopePath("a:*") }

func cleanPath(p string) string {
	s := strings.ReplaceAll(p, "\\", "/")
	s = strings.TrimPrefix(s, "./")
	s = strings.TrimSuffix(s, "/")
	return path.Clean(s)
}

// Kind returns the scope's type tag: 'f' file, 'd' directory subtree,
// 'g' glob pattern or 'a' all files.
func (p ScopePath) Kind() byte {
	if len(p) > 1 {
		return p[0]
	}
	return 'f'
}

// Value returns the path or pattern carried by the scope.
func (p ScopePath) Value() string {
	if len(p) < 2 {
		return ""
	}
	return string(p[2:])
}

// Kind is the package-level form of ScopePath.Kind.
func Kind(p ScopePath) byte { return p.Kind() }

// Value is the package-level form of ScopePath.Value.
func Value(p ScopePath) string { return p.Value() }

// Covers reports whether this scope contains another scope or a concrete path.
// It is the single authority for scope overlap: both planners reach it through
// OverlapByPaths and neither parses a ScopePath itself.
func (p ScopePath) Covers(other ScopePath) bool {
	a, b := p.Value(), other.Value()
	if a == "" || b == "" {
		return false
	}
	switch p.Kind() {
	case 'a':
		return true
	case 'g':
		if globMatch(a, b) {
			return true
		}
		if other.Kind() == 'g' {
			return globMatch(b, a) || commonPrefix(a, b)
		}
		return false
	case 'd':
		return a == b || strings.HasPrefix(b, a+"/") ||
			(other.Kind() == 'g' && strings.HasPrefix(b, a))
	default:
		return a == b || (other.Kind() == 'g' && globMatch(b, a))
	}
}

// CoversPath reports whether a concrete file path falls inside this scope.
func (p ScopePath) CoversPath(file string) bool {
	return p.Covers(ScopeFile(file))
}

// globMatch implements doublestar path matching with the standard library:
// '**' spans zero or more path segments and each other segment follows
// path.Match rules. core cannot import doublestar, so the semantics live here.
func globMatch(pattern, name string) bool {
	ps, ns := strings.Split(pattern, "/"), strings.Split(name, "/")
	var match func(i, j int) bool
	match = func(i, j int) bool {
		if i == len(ps) {
			return j == len(ns)
		}
		if ps[i] == "**" {
			for k := j; k <= len(ns); k++ {
				if match(i+1, k) {
					return true
				}
			}
			return false
		}
		if j == len(ns) {
			return false
		}
		ok, err := path.Match(ps[i], ns[j])
		if err != nil || !ok {
			return false
		}
		return match(i+1, j+1)
	}
	return match(0, 0)
}

// commonPrefix reports whether two glob patterns can describe overlapping
// paths. A segment agrees when the two strings are equal or either one
// contains a wildcard.
func commonPrefix(a, b string) bool {
	pa, pb := strings.Split(a, "/"), strings.Split(b, "/")
	n := len(pa)
	if len(pb) < n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		if pa[i] != pb[i] && !strings.ContainsAny(pa[i], "*?") && !strings.ContainsAny(pb[i], "*?") {
			return false
		}
	}
	return true
}

type TaskStatus string

const (
	TaskAvailable TaskStatus = "available"
	TaskClaimed   TaskStatus = "in_progress"
	TaskDone      TaskStatus = "done"
	TaskDropped   TaskStatus = "dropped"
)

type TaskKind string

const (
	TaskKindTask TaskKind = "task"
	TaskKindEpic TaskKind = "epic"
)

// ReviewMode selects how an epic pull request is approved before it merges.
// An unset value means the LLM reviewer.
type ReviewMode string

const (
	ReviewModeLLM   ReviewMode = "llm"
	ReviewModeHuman ReviewMode = "human"
	ReviewModeNone  ReviewMode = "none"
)

// ChildSpec is one child declared in an epic's plan.
type ChildSpec struct {
	Title    string
	Body     string
	Scope    []ScopePath
	Depends  []TaskID
	Priority int
}

// ChildTaskID returns the deterministic ID of the index-th child of an epic.
// Index is 1-based: children are <epic-id>-01, <epic-id>-02 and so on.
func ChildTaskID(epicID TaskID, index int) TaskID {
	return TaskID(fmt.Sprintf("%s-%02d", epicID, index))
}

type Task struct {
	ID            TaskID
	Title         string
	Body          string
	Priority      int
	Scope         []ScopePath
	DependsOn     []TaskID
	Status        TaskStatus
	SourceRef     string
	Kind          TaskKind
	BaseBranch    string
	RequireReview ReviewMode
	WorkerAgent   string
	WorkerPrompt  string
	Plan          []ChildSpec
}

func (t Task) DependsOnID(id TaskID) bool {
	for _, d := range t.DependsOn {
		if d == id {
			return true
		}
	}
	return false
}

func (t Task) Overlaps(other Task) bool {
	return t.ID == other.ID || OverlapByDependencies(t, other) || OverlapByPaths(t, other)
}

func OverlapByPaths(a, b Task) bool {
	for _, x := range a.Scope {
		for _, y := range b.Scope {
			if x.Covers(y) || y.Covers(x) {
				return true
			}
		}
	}
	return false
}

func OverlapByDependencies(a, b Task) bool {
	return a.DependsOnID(b.ID) || b.DependsOnID(a.ID)
}

type OverlapChecker func(a, b Task) bool

func PathsAndDependencies(a, b Task) bool {
	return OverlapByPaths(a, b) || OverlapByDependencies(a, b)
}

func SortTasks(tasks []Task) {
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Priority != tasks[j].Priority {
			return tasks[i].Priority > tasks[j].Priority
		}
		return tasks[i].ID < tasks[j].ID
	})
}

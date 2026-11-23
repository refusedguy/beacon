// Package orchestrator coordinates maintenance of pull requests that still
// hold an active Watch after another PR has been merged. This file classifies
// the reasons a branch cannot be merged yet. The classification is pure: the
// caller collects the git facts and this package never runs git.
package orchestrator

import (
	"sort"
	"strings"

	"github.com/refusedguy/beacon/internal/shared"
)

// MergeProblemClass names one reason a merge is not safe to start. The four
// values mirror the dmux merge-validation catalogue recorded in CD-15.
type MergeProblemClass string

const (
	ProblemMainDirty      MergeProblemClass = "main_dirty"
	ProblemWorktreeDirty  MergeProblemClass = "worktree_uncommitted"
	ProblemMergeConflict  MergeProblemClass = "merge_conflict"
	ProblemNothingToMerge MergeProblemClass = "nothing_to_merge"
)

// MergeFacts is the git state the preflight needs, already collected by the
// caller. Every field is a value read from a snapshot; none of them starts a
// command or opens a store. CommitsAhead counts commits in the worktree branch
// that main does not have yet.
type MergeFacts struct {
	MainBranch     string
	WorktreeBranch string
	MainFiles      []string
	WorktreeFiles  []string
	CommitsAhead   int
	HasConflict    bool
	ConflictFiles  []string
}

// MergeProblem is one obstruction found before the merge API is called. Files
// lists the paths that caused the problem and is empty for a problem that has
// no paths, such as nothing_to_merge.
type MergeProblem struct {
	Class          MergeProblemClass `json:"class"`
	Message        string            `json:"message"`
	Files          []string          `json:"files,omitempty"`
	CanAutoResolve bool              `json:"canAutoResolve"`
}

// MergePreflightResult is the observable decision. CanMerge is true only when
// no problem was found, so a single unresolved issue keeps the PR out of the
// GitHub merge call.
type MergePreflightResult struct {
	CanMerge bool
	Problems []MergeProblem
}

// mergeConflictPlaceholder keeps a conflict identifiable when the collector
// found one but could not name the paths.
const mergeConflictPlaceholder = "(conflict detection incomplete)"

// PreflightMerge classifies collected git facts into merge problems. ToolPaths
// names paths beacon created itself, such as precheck state or wiring
// markers; they are not user dirt and never appear in a problem's Files list.
// A path given as a directory also hides the files below it.
func PreflightMerge(facts MergeFacts, toolPaths []string) MergePreflightResult {
	mainFiles := mergeVisibleFiles(facts.MainFiles, toolPaths)
	worktreeFiles := mergeVisibleFiles(facts.WorktreeFiles, toolPaths)

	var problems []MergeProblem
	if len(mainFiles) > 0 {
		problems = append(problems, MergeProblem{
			Class:          ProblemMainDirty,
			Message:        "Main branch " + mergeMainLabel(facts.MainBranch) + " has uncommitted changes",
			Files:          mainFiles,
			CanAutoResolve: true,
		})
	}
	if len(worktreeFiles) > 0 {
		problems = append(problems, MergeProblem{
			Class:          ProblemWorktreeDirty,
			Message:        "Worktree has uncommitted changes",
			Files:          worktreeFiles,
			CanAutoResolve: true,
		})
	}

	// Dirty main and worktree files are reported on their own. Counting them
	// here as well would classify a dirty repo as "nothing to merge" and skip
	// a branch that still has changes worth carrying into main.
	if facts.CommitsAhead <= 0 && len(mainFiles) == 0 && len(worktreeFiles) == 0 {
		problems = append(problems, MergeProblem{
			Class:          ProblemNothingToMerge,
			Message:        "No new commits to merge",
			CanAutoResolve: false,
		})
	}

	if facts.HasConflict || len(facts.ConflictFiles) > 0 {
		conflictFiles := mergeVisibleFiles(facts.ConflictFiles, nil)
		if len(conflictFiles) == 0 {
			conflictFiles = []string{mergeConflictPlaceholder}
		}
		problems = append(problems, MergeProblem{
			Class:          ProblemMergeConflict,
			Message:        "Merge conflicts detected",
			Files:          conflictFiles,
			CanAutoResolve: true,
		})
	}

	return MergePreflightResult{
		CanMerge: len(problems) == 0,
		Problems: problems,
	}
}

func mergeMainLabel(branch string) string {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "current branch"
	}
	return branch
}

// mergeVisibleFiles drops tool-created paths, then sorts and deduplicates the
// rest so a problem's file list is stable between ticks.
func mergeVisibleFiles(files, toolPaths []string) []string {
	ignored := make([]string, 0, len(toolPaths))
	for _, p := range toolPaths {
		p = strings.TrimSpace(strings.TrimSuffix(p, "/"))
		if p != "" {
			ignored = append(ignored, p)
		}
	}

	trimmed := make([]string, 0, len(files))
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f != "" {
			trimmed = append(trimmed, f)
		}
	}
	visible := shared.Filter(trimmed, func(f string) bool {
		for _, p := range ignored {
			if f == p || strings.HasPrefix(f, p+"/") {
				return false
			}
		}
		return true
	})
	visible = shared.DistinctBy(visible, func(f string) string { return f })
	sort.Strings(visible)
	return visible
}

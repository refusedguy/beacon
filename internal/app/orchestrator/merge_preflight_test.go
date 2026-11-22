package orchestrator

import (
	"strings"
	"testing"
)

func mergeClasses(result MergePreflightResult) []string {
	classes := make([]string, 0, len(result.Problems))
	for _, p := range result.Problems {
		classes = append(classes, string(p.Class))
	}
	return classes
}

func assertMergeClasses(t *testing.T, result MergePreflightResult, want ...MergeProblemClass) {
	t.Helper()
	got := mergeClasses(result)
	wantStrings := make([]string, 0, len(want))
	for _, c := range want {
		wantStrings = append(wantStrings, string(c))
	}
	if strings.Join(got, ",") != strings.Join(wantStrings, ",") {
		t.Fatalf("problem classes = %v, want %v", got, wantStrings)
	}
}

func mergeProblem(t *testing.T, result MergePreflightResult, class MergeProblemClass) MergeProblem {
	t.Helper()
	for _, p := range result.Problems {
		if p.Class == class {
			return p
		}
	}
	t.Fatalf("no problem with class %q in %v", class, mergeClasses(result))
	return MergeProblem{}
}

func assertMergeFiles(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v", got, want)
	}
}

func TestPreflightMergeDirtyMainAndWorktree(t *testing.T) {
	result := PreflightMerge(MergeFacts{
		MainBranch:    "main",
		MainFiles:     []string{"docs/tasks/CD-15.md", ".gocache/build"},
		WorktreeFiles: []string{"internal/app/orchestrator/merge_preflight.go"},
		CommitsAhead:  1,
	}, nil)

	if result.CanMerge {
		t.Fatal("CanMerge = true for a dirty main and an uncommitted worktree")
	}
	assertMergeClasses(t, result, ProblemMainDirty, ProblemWorktreeDirty)
	main := mergeProblem(t, result, ProblemMainDirty)
	if !main.CanAutoResolve {
		t.Fatalf("main_dirty.CanAutoResolve = false, want true")
	}
	assertMergeFiles(t, main.Files, []string{".gocache/build", "docs/tasks/CD-15.md"})
	worktree := mergeProblem(t, result, ProblemWorktreeDirty)
	if !worktree.CanAutoResolve {
		t.Fatalf("worktree_uncommitted.CanAutoResolve = false, want true")
	}
	assertMergeFiles(t, worktree.Files, []string{"internal/app/orchestrator/merge_preflight.go"})
}

func TestPreflightMergeNothingToMerge(t *testing.T) {
	result := PreflightMerge(MergeFacts{}, nil)

	if result.CanMerge {
		t.Fatal("CanMerge = true with no commits and no changes")
	}
	assertMergeClasses(t, result, ProblemNothingToMerge)
	problem := mergeProblem(t, result, ProblemNothingToMerge)
	if problem.CanAutoResolve {
		t.Fatalf("nothing_to_merge.CanAutoResolve = true, want false")
	}
	if len(problem.Files) != 0 {
		t.Fatalf("nothing_to_merge.Files = %v, want none", problem.Files)
	}
}

func TestPreflightMergeToolPathsAreNotDirt(t *testing.T) {
	toolPaths := []string{"state/precheck.json", "docs/tasks/WIRING-99.md"}
	result := PreflightMerge(MergeFacts{
		MainFiles:     []string{"state/precheck.json", "docs/tasks/WIRING-99.md", "README.md"},
		WorktreeFiles: []string{"state/precheck.json"},
		CommitsAhead:  2,
	}, toolPaths)

	assertMergeClasses(t, result, ProblemMainDirty)
	problem := mergeProblem(t, result, ProblemMainDirty)
	assertMergeFiles(t, problem.Files, []string{"README.md"})
	for _, p := range result.Problems {
		for _, f := range p.Files {
			if f == "state/precheck.json" || f == "docs/tasks/WIRING-99.md" {
				t.Fatalf("tool-created path %q leaked into problem files", f)
			}
		}
	}
}

func TestPreflightMergeOnlyToolChangesIsNothingToMerge(t *testing.T) {
	result := PreflightMerge(MergeFacts{
		MainFiles:     []string{"state/precheck.json"},
		WorktreeFiles: []string{"docs/tasks/WIRING-99.md"},
	}, []string{"state/precheck.json", "docs/tasks/WIRING-99.md"})

	assertMergeClasses(t, result, ProblemNothingToMerge)
}

func TestPreflightMergeDirectoryToolPathHidesChildren(t *testing.T) {
	result := PreflightMerge(MergeFacts{
		MainFiles: []string{"state/precheck.json", "state/precheck.lock", "Makefile"},
	}, []string{"state/"})

	assertMergeClasses(t, result, ProblemMainDirty)
	assertMergeFiles(t, mergeProblem(t, result, ProblemMainDirty).Files, []string{"Makefile"})
}

func TestPreflightMergeDirtyMainIsNotNothingToMerge(t *testing.T) {
	result := PreflightMerge(MergeFacts{
		MainFiles:    []string{"go.mod"},
		CommitsAhead: 0,
	}, nil)

	assertMergeClasses(t, result, ProblemMainDirty)
}

func TestPreflightMergeConflict(t *testing.T) {
	result := PreflightMerge(MergeFacts{
		CommitsAhead:  3,
		HasConflict:   true,
		ConflictFiles: []string{"internal/app/orchestrator/rebase.go", "internal/domain/watch.go"},
	}, nil)

	if result.CanMerge {
		t.Fatal("CanMerge = true with a detected conflict")
	}
	assertMergeClasses(t, result, ProblemMergeConflict)
	problem := mergeProblem(t, result, ProblemMergeConflict)
	if !problem.CanAutoResolve {
		t.Fatalf("merge_conflict.CanAutoResolve = false, want true")
	}
	assertMergeFiles(t, problem.Files, []string{"internal/app/orchestrator/rebase.go", "internal/domain/watch.go"})
}

func TestPreflightMergeConflictWithoutFilesUsesPlaceholder(t *testing.T) {
	result := PreflightMerge(MergeFacts{HasConflict: true, CommitsAhead: 1}, nil)

	assertMergeClasses(t, result, ProblemMergeConflict)
	assertMergeFiles(t, mergeProblem(t, result, ProblemMergeConflict).Files, []string{mergeConflictPlaceholder})
}

func TestPreflightMergeCleanBranchWithCommitsCanMerge(t *testing.T) {
	result := PreflightMerge(MergeFacts{CommitsAhead: 2}, nil)

	if !result.CanMerge {
		t.Fatalf("CanMerge = false with no problems: %v", mergeClasses(result))
	}
	if len(result.Problems) != 0 {
		t.Fatalf("problems = %v, want none", mergeClasses(result))
	}
}

func TestPreflightMergeSortsAndDedupsFiles(t *testing.T) {
	result := PreflightMerge(MergeFacts{
		WorktreeFiles: []string{"z.go", "a.go", "z.go", " ", "m.go"},
		CommitsAhead:  1,
	}, nil)

	assertMergeClasses(t, result, ProblemWorktreeDirty)
	assertMergeFiles(t, mergeProblem(t, result, ProblemWorktreeDirty).Files, []string{"a.go", "m.go", "z.go"})
}

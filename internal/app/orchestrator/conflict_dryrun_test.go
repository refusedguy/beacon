package orchestrator

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

const (
	dryRunBase   = "base-sha"
	dryRunOurs   = "ours-sha"
	dryRunTheirs = "theirs-sha"
)

// Two edits to the same line produce a diff --cc hunk with conflict markers.
// The parser must return just the file name, not the hunk text.
const dryRunTwoEditsOneLine = `merge-tree-tree-oid
diff --cc src/config.go
index 1111111,2222222..0000000
--- a/src/config.go
+++ b/src/config.go
@@@ -1,2 -1,2 +1,6 @@@
  package config
++<<<<<<< ours
 +const Value = "ours"
++=======
++const Value = "theirs"
++>>>>>>> feature
`

// A linear fast-forward has a diff section but no conflict markers.
const dryRunLinearHistory = `merge-tree-tree-oid
diff --git a/src/config.go b/src/config.go
index 1111111..2222222 100644
--- a/src/config.go
+++ b/src/config.go
@@ -1 +1 @@
-const Value = "old"
+const Value = "new"
`

func TestParseMergeTreeConflictsTwoEditsOneLine(t *testing.T) {
	got := ParseMergeTreeConflicts(dryRunTwoEditsOneLine)
	want := []string{"src/config.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("conflicts = %v, want %v", got, want)
	}
}

func TestParseMergeTreeConflictsDiffGitHeader(t *testing.T) {
	out := `diff --git a/src/a.go b/src/a.go
index 111..222 100644
--- a/src/a.go
+++ b/src/a.go
@@ -1 +1,5 @@
 line
+<<<<<<< ours
+ours
+=======
+theirs
+>>>>>>> feature
 line
`
	got := ParseMergeTreeConflicts(out)
	want := []string{"src/a.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("conflicts = %v, want %v", got, want)
	}
}

func TestParseMergeTreeConflictsNoMarkers(t *testing.T) {
	if got := ParseMergeTreeConflicts(dryRunLinearHistory); len(got) != 0 {
		t.Fatalf("linear history conflicts = %v, want empty", got)
	}
}

func TestParseMergeTreeConflictsMissingFileName(t *testing.T) {
	out := `CONFLICT (content): Merge conflict in src/a.go
<<<<<<< ours
ours
=======
theirs
>>>>>>> feature
`
	if got := ParseMergeTreeConflicts(out); len(got) != 0 {
		t.Fatalf("markers without a diff header conflicts = %v, want empty", got)
	}
}

func TestParseMergeTreeConflictsDeduplicatesAndKeepsOrder(t *testing.T) {
	out := `diff --git a/src/b.go b/src/b.go
--- a/src/b.go
+++ b/src/b.go
@@ -1 +1,3 @@
+<<<<<<< ours
+>>>>>>> theirs
diff --git a/src/a.go b/src/a.go
--- a/src/a.go
+++ b/src/a.go
@@ -1 +1,3 @@
+<<<<<<< ours
+>>>>>>> theirs
diff --git a/src/b.go b/src/b.go
--- a/src/b.go
+++ b/src/b.go
@@ -1 +1,3 @@
+<<<<<<< ours
+>>>>>>> theirs
`
	got := ParseMergeTreeConflicts(out)
	want := []string{"src/b.go", "src/a.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("conflicts = %v, want %v", got, want)
	}
}

func TestFallbackChangedFilesDiverged(t *testing.T) {
	got := FallbackChangedFiles(2, 3,
		[]string{"src/a.go", "src/b.go"},
		[]string{"src/b.go", "src/c.go"},
	)
	if !got.Diverged {
		t.Fatal("diverged = false, want true")
	}
	want := []string{"src/a.go", "src/b.go", "src/c.go"}
	if !reflect.DeepEqual(got.Changed, want) {
		t.Fatalf("changed = %v, want %v", got.Changed, want)
	}
	if !got.Conflict() {
		t.Fatal("diverged fallback must report a conflict")
	}
}

func TestFallbackChangedFilesLinearHistory(t *testing.T) {
	tests := []struct {
		name          string
		behind, ahead int
	}{
		{name: "fast forward", behind: 1, ahead: 0},
		{name: "only ours moved", behind: 0, ahead: 3},
		{name: "no divergence", behind: 0, ahead: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FallbackChangedFiles(tt.behind, tt.ahead, []string{"src/a.go"}, []string{"src/b.go"})
			if got.Conflict() || got.Diverged || len(got.Changed) != 0 {
				t.Fatalf("result = %+v, want empty", got)
			}
		})
	}
}

func TestDryRunGitCheckReadOnly(t *testing.T) {
	var calls [][]string
	checker := &DryRunGit{Git: GitRunnerFunc(func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, append([]string{}, args...))
		return dryRunTwoEditsOneLine, nil
	})}

	res, err := checker.Check(context.Background(), "", dryRunBase, dryRunOurs, dryRunTheirs)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	want := []string{"src/config.go"}
	if !reflect.DeepEqual(res.Files, want) {
		t.Fatalf("files = %v, want %v", res.Files, want)
	}
	if len(calls) != 1 || calls[0][0] != "merge-tree" {
		t.Fatalf("calls = %v, want one merge-tree call", calls)
	}
	assertReadOnlyGitCalls(t, calls)
}

func TestDryRunGitCheckFallbackIsReadOnly(t *testing.T) {
	var calls [][]string
	checker := &DryRunGit{Git: GitRunnerFunc(func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, append([]string{}, args...))
		switch args[0] {
		case "merge-tree":
			return "", errors.New("merge-tree not available")
		case "rev-list":
			return "2 1\n", nil
		case "diff":
			if args[len(args)-1] == dryRunOurs {
				return "src/a.go\nsrc/b.go\n", nil
			}
			return "src/b.go\nsrc/c.go\n", nil
		default:
			return "", errors.New("unexpected command " + args[0])
		}
	})}

	res, err := checker.Check(context.Background(), "", dryRunBase, dryRunOurs, dryRunTheirs)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Diverged || !res.Conflict() {
		t.Fatalf("result = %+v, want diverged conflict", res)
	}
	want := []string{"src/a.go", "src/b.go", "src/c.go"}
	if !reflect.DeepEqual(res.Changed, want) {
		t.Fatalf("changed = %v, want %v", res.Changed, want)
	}
	if len(calls) != 4 {
		t.Fatalf("calls = %v, want four read-only git calls", calls)
	}
	assertReadOnlyGitCalls(t, calls)
}

func TestDryRunGitCheckRequiresRunner(t *testing.T) {
	if _, err := (&DryRunGit{}).Check(context.Background(), "", dryRunBase, dryRunOurs, dryRunTheirs); err == nil {
		t.Fatal("Check without a git runner must fail")
	}
}

func assertReadOnlyGitCalls(t *testing.T, calls [][]string) {
	t.Helper()
	for _, call := range calls {
		if len(call) == 0 {
			t.Fatal("empty git call")
		}
		switch call[0] {
		case "merge-tree", "rev-list", "diff":
			continue
		default:
			t.Fatalf("tree-modifying git command called: %v", call)
		}
	}
}

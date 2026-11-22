package worktree

import (
	"errors"
	"fmt"
	"testing"
)

// guardGit answers the four git calls the collector makes. It records every
// ls-files and worktree remove call so a test can prove which guard fired.
type guardGit struct {
	list       string
	status     map[string]string
	ignored    map[string]string
	ignoredErr map[string]error
	removed    []string
	lsCalls    [][]string
}

func (f *guardGit) run(dir string, args ...string) (string, error) {
	switch {
	case len(args) >= 2 && args[0] == "worktree" && args[1] == "list":
		return f.list, nil
	case len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain":
		return f.status[dir], nil
	case len(args) >= 1 && args[0] == "ls-files":
		f.lsCalls = append(f.lsCalls, append([]string{dir}, args...))
		if err := f.ignoredErr[dir]; err != nil {
			return "", err
		}
		return f.ignored[dir], nil
	case len(args) >= 4 && args[0] == "worktree" && args[1] == "remove" && args[2] == "--":
		f.removed = append(f.removed, args[3])
		return "", nil
	default:
		return "", fmt.Errorf("unexpected git call: dir=%q args=%q", dir, args)
	}
}

func assertLsFilesFlags(t *testing.T, f *guardGit) {
	t.Helper()
	if len(f.lsCalls) == 0 {
		t.Fatal("no ls-files call recorded")
	}
	want := []string{"ls-files", "--others", "--ignored", "--exclude-standard", "-z"}
	for _, call := range f.lsCalls {
		if len(call) != len(want)+1 {
			t.Fatalf("ls-files call = %q, want dir plus %q", call, want)
		}
		for i, flag := range want {
			if call[i+1] != flag {
				t.Fatalf("ls-files call = %q, want flag %q at %d", call, flag, i+1)
			}
		}
	}
}

func TestCollectKeepsWorktreeWithIgnoredFile(t *testing.T) {
	dir := t.TempDir()
	f := &guardGit{
		list: fmt.Sprintf("worktree /main\nHEAD %040d\nbranch refs/heads/main\n\n"+
			"worktree %s\nHEAD %040d\nbranch refs/heads/ignored\n\n", 0, dir, 0),
		status:  map[string]string{dir: ""},
		ignored: map[string]string{dir: "build/\x00cache.tmp\x00"},
	}

	res, err := Collect(f.run, nil)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}

	assertRemoved(t, res)
	assertKept(t, res, map[string]string{dir: ReasonIgnoredFiles})
	if len(f.removed) != 0 {
		t.Fatalf("git worktree remove calls = %v, want none", f.removed)
	}
	assertLsFilesFlags(t, f)
}

func TestRemoveCleanWorktreeOnce(t *testing.T) {
	dir := t.TempDir()
	f := &guardGit{ignored: map[string]string{dir: ""}}

	if err := Remove(f.run, dir); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if len(f.removed) != 1 || f.removed[0] != dir {
		t.Fatalf("git worktree remove calls = %v, want [%s]", f.removed, dir)
	}
	assertLsFilesFlags(t, f)
}

func TestRemoveFromInsideWorktreeIsBlocked(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	f := &guardGit{ignored: map[string]string{dir: ""}}

	err := Remove(f.run, dir)
	if err == nil {
		t.Fatal("Remove() error = nil, want blocked")
	}
	var blocked *BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("Remove() error = %T, want *BlockedError", err)
	}
	if blocked.Reason != ReasonInsideWorktree {
		t.Fatalf("blocked reason = %q, want %q", blocked.Reason, ReasonInsideWorktree)
	}
	if len(f.removed) != 0 {
		t.Fatalf("git worktree remove calls = %v, want none", f.removed)
	}
}

func TestCollectKeepsDirtyAndLockedWorktrees(t *testing.T) {
	dir := t.TempDir()
	locked := t.TempDir()
	f := &guardGit{
		list: fmt.Sprintf("worktree /main\nHEAD %040d\nbranch refs/heads/main\n\n"+
			"worktree %s\nHEAD %040d\nbranch refs/heads/dirty\n\n"+
			"worktree %s\nHEAD %040d\nbranch refs/heads/locked\nlocked\n\n", 0, dir, 0, locked, 0),
		status:  map[string]string{dir: " M file.go\n"},
		ignored: map[string]string{dir: "", locked: ""},
	}

	res, err := Collect(f.run, nil)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}

	assertRemoved(t, res)
	assertKept(t, res, map[string]string{
		dir:    ReasonDirty,
		locked: ReasonLocked,
	})
	if len(f.removed) != 0 {
		t.Fatalf("git worktree remove calls = %v, want none", f.removed)
	}
}

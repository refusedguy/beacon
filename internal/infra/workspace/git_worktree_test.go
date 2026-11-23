package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v: %s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

func newTestGitWorktree(t *testing.T) (*GitWorktree, string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, root, "init", "--bare", "--initial-branch=main", "origin.git")
	gitCmd(t, repo, "init", "--initial-branch=main")
	gitCmd(t, repo, "config", "user.name", "Test User")
	gitCmd(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "add", "README.md")
	gitCmd(t, repo, "commit", "-m", "initial")
	gitCmd(t, repo, "remote", "add", "origin", origin)
	gitCmd(t, repo, "push", "-u", "origin", "main")
	wt, err := New(repo, "main", filepath.Join(root, "worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	return wt, repo
}

func TestGitWorktreeCleanScopesToWorktreeAndProtectsAgentFiles(t *testing.T) {
	wt, repo := newTestGitWorktree(t)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-CLEAN"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if clean, err := wt.Clean(ctx, ws); err != nil || !clean {
		t.Fatalf("Clean() = %v, %v; want true, nil", clean, err)
	}
	if clean, err := wt.Clean(ctx, core.Workspace{}); err == nil || clean {
		t.Fatalf("Clean() with empty path = %v, %v; want false, error", clean, err)
	}
	if clean, err := wt.Clean(ctx, core.Workspace{Path: filepath.Join(ws.Path, "missing")}); err == nil || clean {
		t.Fatalf("Clean() with missing path = %v, %v; want false, error", clean, err)
	}

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("dirty main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if clean, err := wt.Clean(ctx, ws); err != nil || !clean {
		t.Fatalf("Clean() with dirty main repo = %v, %v; want true, nil", clean, err)
	}

	dirty := filepath.Join(ws.Path, "agent.txt")
	if err := os.WriteFile(dirty, []byte("agent work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if clean, err := wt.Clean(ctx, ws); err != nil || clean {
		t.Fatalf("Clean() with dirty worktree = %v, %v; want false, nil", clean, err)
	}
	if err := wt.Remove(ctx, ws); err == nil {
		t.Fatal("Remove() error = nil, want error for dirty worktree")
	}
	if _, err := os.Stat(dirty); err != nil {
		t.Fatalf("agent file removed by Remove(): %v", err)
	}

	_, err = wt.Create(ctx, core.Task{ID: "T-CLEAN"})
	var appErr *core.AppError
	if !errors.As(err, &appErr) || appErr.Code != core.CodeConflict {
		t.Fatalf("Create() error = %v, want *core.AppError with CodeConflict", err)
	}
	if _, err := os.Stat(dirty); err != nil {
		t.Fatalf("agent file removed by Create(): %v", err)
	}
}

func TestGitWorktreeRemoveCleanWorktreeAndRecreate(t *testing.T) {
	wt, _ := newTestGitWorktree(t)
	ctx := context.Background()
	task := core.Task{ID: "T-RECREATE"}
	ws, err := wt.Create(ctx, task)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := wt.Remove(ctx, ws); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree path still exists after Remove(): %v", err)
	}
	if _, err := wt.Create(ctx, task); err != nil {
		t.Fatalf("Create() after Remove() error = %v", err)
	}
}

func TestGitWorktreeCommitPathOnlyStagesPathspec(t *testing.T) {
	wt, _ := newTestGitWorktree(t)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-COMMIT"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	headBefore := strings.TrimSpace(gitCmd(t, ws.Path, "rev-parse", "HEAD"))
	if err := wt.CommitPath(ctx, ws, "docs/tasks", "beacon: complete task T-COMMIT"); err != nil {
		t.Fatalf("CommitPath() with no diff error = %v", err)
	}
	if got := strings.TrimSpace(gitCmd(t, ws.Path, "rev-parse", "HEAD")); got != headBefore {
		t.Fatalf("CommitPath() with no diff created commit %s", got)
	}

	cardDir := filepath.Join(ws.Path, "docs", "tasks")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cardDir, "T-COMMIT.md"), []byte("---\nid: T-COMMIT\nstatus: done\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, "agent.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wt.CommitPath(ctx, ws, "docs/tasks", "beacon: complete task T-COMMIT"); err != nil {
		t.Fatalf("CommitPath() error = %v", err)
	}
	show := gitCmd(t, ws.Path, "show", "--pretty=format:%s", "--name-only", "HEAD")
	if !strings.Contains(show, "beacon: complete task T-COMMIT") {
		t.Fatalf("commit subject missing: %s", show)
	}
	if !strings.Contains(show, "docs/tasks/T-COMMIT.md") {
		t.Fatalf("card not in commit: %s", show)
	}
	if strings.Contains(show, "agent.go") {
		t.Fatalf("agent code staged in commit: %s", show)
	}
	if status := gitCmd(t, ws.Path, "status", "--porcelain", "--", "agent.go"); strings.TrimSpace(status) == "" {
		t.Fatal("agent file was committed instead of left in the worktree")
	}
	local := strings.TrimSpace(gitCmd(t, ws.Path, "rev-parse", "HEAD"))
	remote := strings.TrimSpace(gitCmd(t, ws.Path, "ls-remote", "origin", "refs/heads/"+ws.Branch))
	if remote == "" || !strings.HasPrefix(remote, local) {
		t.Fatalf("branch not pushed: local=%s remote=%q", local, remote)
	}
	if err := wt.CommitPath(ctx, ws, "docs/tasks", "beacon: complete task T-COMMIT"); err != nil {
		t.Fatalf("second CommitPath() error = %v", err)
	}
	if got := strings.TrimSpace(gitCmd(t, ws.Path, "rev-parse", "HEAD")); got != local {
		t.Fatalf("second CommitPath() created commit %s", got)
	}
}

func TestGitWorktreeHasChanges(t *testing.T) {
	wt, _ := newTestGitWorktree(t)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-AHEAD"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if changed, err := wt.HasChanges(ctx, ws, "main"); err != nil || changed {
		t.Fatalf("HasChanges() = %v, %v; want false, nil", changed, err)
	}
	if changed, err := wt.HasChanges(ctx, core.Workspace{}, "main"); err == nil || changed {
		t.Fatalf("HasChanges() with empty path = %v, %v; want false, error", changed, err)
	}

	dirty := filepath.Join(ws.Path, "WIP.txt")
	if err := os.WriteFile(dirty, []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := wt.HasChanges(ctx, ws, "main"); err != nil || !changed {
		t.Fatalf("HasChanges() with uncommitted file = %v, %v; want true, nil", changed, err)
	}
	if err := os.Remove(dirty); err != nil {
		t.Fatal(err)
	}
	if changed, err := wt.HasChanges(ctx, ws, "main"); err != nil || changed {
		t.Fatalf("HasChanges() after cleanup = %v, %v; want false, nil", changed, err)
	}

	if err := os.WriteFile(filepath.Join(ws.Path, "README.md"), []byte("agent commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, ws.Path, "add", "README.md")
	gitCmd(t, ws.Path, "commit", "-m", "agent commit")
	if clean, err := wt.Clean(ctx, ws); err != nil || !clean {
		t.Fatalf("Clean() with commit-only change = %v, %v; want true, nil", clean, err)
	}
	if changed, err := wt.HasChanges(ctx, ws, "main"); err != nil || !changed {
		t.Fatalf("HasChanges() with commit-only change = %v, %v; want true, nil", changed, err)
	}
}

package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/app"
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

func TestGitWorktreeCreateUsesTaskBaseBranch(t *testing.T) {
	wt, repo := newTestGitWorktree(t)
	ctx := context.Background()

	gitCmd(t, repo, "checkout", "-b", "release/1")
	if err := os.WriteFile(filepath.Join(repo, "base-only.txt"), []byte("release\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "add", "base-only.txt")
	gitCmd(t, repo, "commit", "-m", "release base")
	releaseSHA := strings.TrimSpace(gitCmd(t, repo, "rev-parse", "HEAD"))
	gitCmd(t, repo, "push", "origin", "release/1")
	gitCmd(t, repo, "checkout", "main")

	ws, err := wt.Create(ctx, core.Task{ID: "T-CUSTOM-BASE", BaseBranch: "release/1"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if ws.Base != releaseSHA {
		t.Fatalf("workspace base = %q, want %q", ws.Base, releaseSHA)
	}
	if head := strings.TrimSpace(gitCmd(t, ws.Path, "rev-parse", "HEAD")); head != releaseSHA {
		t.Fatalf("worktree HEAD = %q, want %q", head, releaseSHA)
	}
	if _, err := os.Stat(filepath.Join(ws.Path, "base-only.txt")); err != nil {
		t.Fatalf("worktree missing base-branch file: %v", err)
	}

	mainWS, err := wt.Create(ctx, core.Task{ID: "T-DEFAULT-BASE"})
	if err != nil {
		t.Fatalf("Create() without BaseBranch error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(mainWS.Path, "base-only.txt")); !os.IsNotExist(err) {
		t.Fatalf("default worktree used release branch: %v", err)
	}
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

func TestGitWorktreeCommitPathRetryKeepsSingleCommit(t *testing.T) {
	wt, _ := newTestGitWorktree(t)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-RETRY"})
	if err != nil {
		t.Fatal(err)
	}
	cardDir := filepath.Join(ws.Path, "docs", "tasks")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cardDir, "T-RETRY.md"), []byte("card\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wt.CommitPath(ctx, ws, "docs/tasks", "beacon: create task T-RETRY"); err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(gitCmd(t, ws.Path, "rev-parse", "HEAD"))

	// A retry with no new diff must not create a second commit, but still pushes.
	if err := wt.CommitPath(ctx, ws, "docs/tasks", "beacon: create task T-RETRY"); err != nil {
		t.Fatalf("retry CommitPath() error = %v", err)
	}
	if got := strings.TrimSpace(gitCmd(t, ws.Path, "rev-parse", "HEAD")); got != head {
		t.Fatalf("retry created a commit: HEAD %s -> %s", head, got)
	}
	if count := strings.TrimSpace(gitCmd(t, ws.Path, "rev-list", "--count", "--grep=beacon: create task T-RETRY", "HEAD")); count != "1" {
		t.Fatalf("card commits = %s, want 1", count)
	}
}

func TestGitWorktreeRebaseBaseMakesStaleBasePushable(t *testing.T) {
	wt, repo := newTestGitWorktree(t)
	ctx := context.Background()
	root := filepath.Dir(repo)
	origin := filepath.Join(root, "origin.git")

	// Advance origin/main from a second checkout, leaving the root repo behind.
	other := filepath.Join(root, "other")
	gitCmd(t, root, "clone", origin, other)
	gitCmd(t, other, "config", "user.name", "Test User")
	gitCmd(t, other, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(other, "remote.txt"), []byte("remote change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, other, "add", "remote.txt")
	gitCmd(t, other, "commit", "-m", "remote change")
	gitCmd(t, other, "push", "origin", "main")

	// The card is written before the rebase, as in Create. Git tolerates the
	// untracked card file since it does not overlap the rebased history.
	cardDir := filepath.Join(repo, "docs", "tasks")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cardDir, "T-REBASE.md"), []byte("card\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rootWS := core.Workspace{Path: repo, Branch: "main"}
	if err := wt.RebaseBase(ctx, rootWS, "main"); err != nil {
		t.Fatalf("RebaseBase() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "remote.txt")); err != nil {
		t.Fatalf("remote change missing after rebase: %v", err)
	}
	if err := wt.CommitPath(ctx, rootWS, "docs/tasks", "beacon: create task T-REBASE"); err != nil {
		t.Fatalf("CommitPath() error = %v", err)
	}

	log := gitCmd(t, origin, "log", "--oneline", "-2", "main")
	if !strings.Contains(log, "create task T-REBASE") || !strings.Contains(log, "remote change") {
		t.Fatalf("origin/main missing rebased card:\n%s", log)
	}
}

func TestGitWorktreeCommitPathReportsNonFastForward(t *testing.T) {
	wt, repo := newTestGitWorktree(t)
	ctx := context.Background()
	root := filepath.Dir(repo)
	origin := filepath.Join(root, "origin.git")
	ws, err := wt.Create(ctx, core.Task{ID: "T-REJECT"})
	if err != nil {
		t.Fatal(err)
	}

	// Create a divergent remote branch from a second checkout.
	other := filepath.Join(root, "other-reject")
	gitCmd(t, root, "clone", origin, other)
	gitCmd(t, other, "config", "user.name", "Test User")
	gitCmd(t, other, "config", "user.email", "test@example.com")
	gitCmd(t, other, "checkout", "-b", ws.Branch, "origin/main")
	if err := os.WriteFile(filepath.Join(other, "divergent.txt"), []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, other, "add", "divergent.txt")
	gitCmd(t, other, "commit", "-m", "divergent change")
	gitCmd(t, other, "push", "origin", ws.Branch)

	cardDir := filepath.Join(ws.Path, "docs", "tasks")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cardDir, "T-REJECT.md"), []byte("card\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = wt.CommitPath(ctx, ws, "docs/tasks", "beacon: create task T-REJECT")
	var nff *app.NonFastForwardError
	if !errors.As(err, &nff) {
		t.Fatalf("CommitPath() error = %T %v, want *app.NonFastForwardError", err, err)
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

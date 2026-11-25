package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/harnesses/opencode"
	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

type refreshRecorder struct {
	mu        sync.Mutex
	refreshes []map[string]any
	reloads   int
	status    int
}

func (r *refreshRecorder) snapshot() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.refreshes...)
}

func (r *refreshRecorder) reloadCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reloads
}

func newRefreshServer(t *testing.T, canonical string, status int) (*httptest.Server, *refreshRecorder) {
	t.Helper()
	rec := &refreshRecorder{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/project":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "prj_beacon", "canonical": canonical}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/worktree/refresh":
			body := map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			rec.mu.Lock()
			rec.refreshes = append(rec.refreshes, body)
			rec.mu.Unlock()
			if rec.status != 0 {
				w.WriteHeader(rec.status)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/location/reload":
			rec.mu.Lock()
			rec.reloads++
			rec.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func worktreeWithRefresher(t *testing.T, status int) (*GitWorktree, *refreshRecorder) {
	t.Helper()
	wt, repo := newTestGitWorktree(t)
	srv, rec := newRefreshServer(t, repo, status)
	h := opencode.New(opencode.Config{Endpoint: srv.URL})
	h.Repo = repo
	wt.Refresher = h
	return wt, rec
}

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

	// A stale path holding real agent code is never discarded on retry.
	if _, err := wt.Create(ctx, core.Task{ID: "T-CLEAN"}); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("Create() with agent code = %v, want CodeConflict", err)
	}
	if _, err := os.Stat(dirty); err != nil {
		t.Fatalf("agent file removed by Create(): %v", err)
	}

	// A stale path holding only Beacon's card status marker is safe to replace.
	if err := os.Remove(dirty); err != nil {
		t.Fatal(err)
	}
	cardDir := filepath.Join(ws.Path, "docs", "tasks")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatal(err)
	}
	card := filepath.Join(cardDir, "T-CLEAN.md")
	if err := os.WriteFile(card, []byte("status: in_progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recreated, err := wt.Create(ctx, core.Task{ID: "T-CLEAN"})
	if err != nil {
		t.Fatalf("Create() with card-only dirt error = %v", err)
	}
	if _, err := os.Stat(card); !os.IsNotExist(err) {
		t.Fatalf("card-only dirt survived Create(): %v", err)
	}
	if clean, err := wt.Clean(ctx, recreated); err != nil || !clean {
		t.Fatalf("recreated worktree Clean() = %v, %v; want true, nil", clean, err)
	}
	if recreated.Branch != ws.Branch {
		t.Fatalf("branch = %q, want %q", recreated.Branch, ws.Branch)
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

func TestGitWorktreeRebaseBaseKeepsDirtyCard(t *testing.T) {
	wt, repo := newTestGitWorktree(t)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-REBASE-CARD"})
	if err != nil {
		t.Fatal(err)
	}
	cardDir := filepath.Join(ws.Path, "docs", "tasks")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatal(err)
	}
	card := filepath.Join(cardDir, "T-NEW.md")
	if err := os.WriteFile(card, []byte("status: available\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "base2.txt"), []byte("advanced\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "add", "base2.txt")
	gitCmd(t, repo, "commit", "-m", "advance base")
	gitCmd(t, repo, "push", "origin", "main")

	if err := wt.RebaseBase(ctx, ws, "main"); err != nil {
		t.Fatalf("RebaseBase() error = %v", err)
	}
	if _, err := os.Stat(card); err != nil {
		t.Fatalf("uncommitted card lost during rebase: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.Path, "base2.txt")); err != nil {
		t.Fatalf("worktree did not pick up the new base: %v", err)
	}
}

func TestGitWorktreeCreateReportsBranchCheckedOutElsewhere(t *testing.T) {
	wt, repo := newTestGitWorktree(t)
	ctx := context.Background()
	root := filepath.Dir(repo)
	task := core.Task{ID: "T-BRANCH-ELSEWHERE"}
	branch := branchName(task.ID)

	// Check the task branch out in a worktree outside the directory Create
	// manages. git then refuses to delete it with `branch -D`.
	elsewhere := filepath.Join(root, "elsewhere")
	gitCmd(t, repo, "worktree", "add", "-b", branch, elsewhere, "origin/main")

	_, err := wt.Create(ctx, task)
	if err == nil {
		t.Fatal("Create() error = nil, want an error when the branch is checked out elsewhere")
	}
	if !strings.Contains(err.Error(), "delete branch "+branch) {
		t.Fatalf("Create() error = %q, want it to name the branch it could not delete", err)
	}
	if !strings.Contains(err.Error(), "cannot delete branch") {
		t.Fatalf("Create() error = %q, want git's refusal detail", err)
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

func TestGitWorktreeCommitPathPushSurvivesCancellationDuringPush(t *testing.T) {
	wt, repo := newTestGitWorktree(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws, err := wt.Create(ctx, core.Task{ID: "T-PUSH-CANCEL"})
	if err != nil {
		t.Fatal(err)
	}
	cardDir := filepath.Join(ws.Path, "docs", "tasks")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cardDir, "T-PUSH-CANCEL.md"), []byte("card\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The hook parks the push after the commit is durable so the caller can
	// cancel mid-push and release it. A cancelled caller must not strand the
	// committed card on the worktree.
	markerDir := t.TempDir()
	started := filepath.Join(markerDir, "started")
	release := filepath.Join(markerDir, "release")
	hook := fmt.Sprintf("#!/bin/sh\ntouch %q\ni=0\nwhile [ ! -f %q ]; do\n  sleep 0.02\n  i=$((i+1))\n  if [ $i -gt 500 ]; then exit 1; fi\ndone\n", started, release)
	hooksDir := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- wt.CommitPath(ctx, ws, "docs/tasks", "beacon: create task T-PUSH-CANCEL")
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pre-push hook never started")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CommitPath() error after caller cancellation: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CommitPath() did not finish after the push was released")
	}
	if ctx.Err() == nil {
		t.Fatal("test did not cancel the caller during the push")
	}
	local := strings.TrimSpace(gitCmd(t, ws.Path, "rev-parse", "HEAD"))
	remote := strings.TrimSpace(gitCmd(t, ws.Path, "ls-remote", "origin", "refs/heads/"+ws.Branch))
	if remote == "" || !strings.HasPrefix(remote, local) {
		t.Fatalf("committed card was not pushed: local=%s remote=%q", local, remote)
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

func TestWorktreeRemoveRefreshesOpenCode(t *testing.T) {
	wt, rec := worktreeWithRefresher(t, 0)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-REFRESH"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.Remove(ctx, ws); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	refreshes := rec.snapshot()
	if len(refreshes) != 1 {
		t.Fatalf("worktree refresh calls = %d, want 1", len(refreshes))
	}
	if got := refreshes[0]["projectID"]; got != "prj_beacon" {
		t.Fatalf("refresh projectID = %v, want prj_beacon", got)
	}
}

func TestWorktreeRemoveRefreshesAfterOutOfBandDelete(t *testing.T) {
	wt, rec := worktreeWithRefresher(t, 0)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-STALE"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(ws.Path); err != nil {
		t.Fatal(err)
	}
	if err := wt.Remove(ctx, ws); err != nil {
		t.Fatalf("Remove() after out-of-band delete = %v, want nil", err)
	}
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("worktree refresh calls = %d, want 1", got)
	}
}

func TestWorktreeRemoveIgnoresRefreshFailure(t *testing.T) {
	wt, rec := worktreeWithRefresher(t, http.StatusInternalServerError)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-REFRESH-FAIL"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.Remove(ctx, ws); err != nil {
		t.Fatalf("Remove() with failing refresh = %v, want nil", err)
	}
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("worktree refresh calls = %d, want 1", got)
	}
}

func TestForceRemoveRefreshesOpenCode(t *testing.T) {
	wt, rec := worktreeWithRefresher(t, 0)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-FORCE-REFRESH"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, "WIP.txt"), []byte("agent work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wt.ForceRemove(ctx, ws); err != nil {
		t.Fatalf("ForceRemove() error = %v", err)
	}
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("worktree refresh calls = %d, want 1", got)
	}
}

// breakGitAdmin removes the worktree's administrative directory. Git then
// refuses both status and worktree remove for that path.
func breakGitAdmin(t *testing.T, wt *GitWorktree, ws core.Workspace) {
	t.Helper()
	admin := filepath.Join(wt.Root, ".git", "worktrees", filepath.Base(ws.Path))
	if err := os.RemoveAll(admin); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeRemoveRefreshesWhenGitCannotRemove(t *testing.T) {
	wt, rec := worktreeWithRefresher(t, 0)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-BROKEN-ADMIN"})
	if err != nil {
		t.Fatal(err)
	}
	breakGitAdmin(t, wt, ws)
	if err := wt.Remove(ctx, ws); err == nil {
		t.Fatal("Remove() error = nil, want an error when git metadata is missing")
	}
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("worktree refresh calls = %d, want 1 after failed removal", got)
	}
}

func TestForceRemoveRefreshesWhenGitCannotRemove(t *testing.T) {
	wt, rec := worktreeWithRefresher(t, 0)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-FORCE-BROKEN"})
	if err != nil {
		t.Fatal(err)
	}
	breakGitAdmin(t, wt, ws)
	if err := wt.ForceRemove(ctx, ws); err == nil {
		t.Fatal("ForceRemove() error = nil, want an error when git metadata is missing")
	}
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("worktree refresh calls = %d, want 1 after failed force removal", got)
	}
}

func TestWorktreeCreateRefreshesAfterStaleCleanup(t *testing.T) {
	wt, rec := worktreeWithRefresher(t, 0)
	ctx := context.Background()
	task := core.Task{ID: "T-CREATE-STALE"}
	if _, err := wt.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	// A retry finds the previous worktree still on disk. Create removes it as
	// stale, so the editor's registration must be refreshed again.
	if _, err := wt.Create(ctx, task); err != nil {
		t.Fatalf("Create() retry error = %v", err)
	}
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("worktree refresh calls = %d, want 1 after stale cleanup", got)
	}
	if got := rec.reloadCount(); got != 1 {
		t.Fatalf("location reloads = %d, want 1 after stale cleanup", got)
	}
}

func TestWorktreeRemovalReloadsLiveEditorLocations(t *testing.T) {
	wt, rec := worktreeWithRefresher(t, 0)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-RELOAD"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.Remove(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("worktree refresh calls = %d, want 1", got)
	}
	if got := rec.reloadCount(); got != 1 {
		t.Fatalf("location reloads = %d, want 1 after a successful refresh", got)
	}
}

func TestWorktreeRefreshFailureSkipsLocationReload(t *testing.T) {
	wt, rec := worktreeWithRefresher(t, http.StatusInternalServerError)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-NO-RELOAD"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.Remove(ctx, ws); err != nil {
		t.Fatalf("Remove() with failing refresh = %v, want nil", err)
	}
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("worktree refresh calls = %d, want 1", got)
	}
	if got := rec.reloadCount(); got != 0 {
		t.Fatalf("location reloads = %d, want 0 when refresh failed", got)
	}
}

func TestGitWorktreeEnsureBranchCreatesBranchChildrenCanFetch(t *testing.T) {
	wt, repo := newTestGitWorktree(t)
	ctx := context.Background()
	origin := filepath.Join(filepath.Dir(repo), "origin.git")
	baseSHA := strings.TrimSpace(gitCmd(t, origin, "rev-parse", "refs/heads/main"))

	if err := wt.EnsureBranch(ctx, "beacon/T-EPIC", "main"); err != nil {
		t.Fatalf("EnsureBranch() error = %v", err)
	}
	if got := strings.TrimSpace(gitCmd(t, origin, "rev-parse", "refs/heads/beacon/T-EPIC")); got != baseSHA {
		t.Fatalf("epic branch = %q, want base %q", got, baseSHA)
	}
	// This is the fetch a child dispatch performs. It used to fail when the
	// epic branch had never been published.
	ws, err := wt.Create(ctx, core.Task{ID: "T-EPIC-01", BaseBranch: "beacon/T-EPIC"})
	if err != nil {
		t.Fatalf("Create() for epic child error = %v", err)
	}
	if ws.Base != baseSHA {
		t.Fatalf("child workspace base = %q, want %q", ws.Base, baseSHA)
	}
}

func TestGitWorktreeEnsureBranchRepeatsAreNoOps(t *testing.T) {
	wt, repo := newTestGitWorktree(t)
	ctx := context.Background()
	origin := filepath.Join(filepath.Dir(repo), "origin.git")
	if err := wt.EnsureBranch(ctx, "beacon/T-EPIC", ""); err != nil {
		t.Fatalf("EnsureBranch() with configured base error = %v", err)
	}
	created := strings.TrimSpace(gitCmd(t, origin, "rev-parse", "refs/heads/beacon/T-EPIC"))

	if err := os.WriteFile(filepath.Join(repo, "advance.txt"), []byte("new base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "add", "advance.txt")
	gitCmd(t, repo, "commit", "-m", "advance main")
	gitCmd(t, repo, "push", "origin", "main")
	if err := wt.EnsureBranch(ctx, "beacon/T-EPIC", "main"); err != nil {
		t.Fatalf("repeated EnsureBranch() error = %v", err)
	}
	if got := strings.TrimSpace(gitCmd(t, origin, "rev-parse", "refs/heads/beacon/T-EPIC")); got != created {
		t.Fatalf("repeated EnsureBranch moved the epic branch to %q, want %q", got, created)
	}
}

func TestGitWorktreeEnsureBranchFailureLeavesNoRemoteBranch(t *testing.T) {
	wt, repo := newTestGitWorktree(t)
	origin := filepath.Join(filepath.Dir(repo), "origin.git")
	if err := wt.EnsureBranch(context.Background(), "beacon/T-EPIC", "missing-base"); err == nil {
		t.Fatal("EnsureBranch() with a missing base error = nil, want an error")
	}
	cmd := exec.Command("git", "-C", origin, "rev-parse", "--verify", "--quiet", "refs/heads/beacon/T-EPIC")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("failed EnsureBranch still published a branch: %s", out)
	}
}

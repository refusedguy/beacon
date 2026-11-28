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
	"sort"
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
		t.Fatalf("os.MkdirAll(origin, 0o755): %v", err)
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(repo, 0o755): %v", err)
	}
	gitCmd(t, root, "init", "--bare", "--initial-branch=main", "origin.git")
	gitCmd(t, repo, "init", "--initial-branch=main")
	gitCmd(t, repo, "config", "user.name", "Test User")
	gitCmd(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(repo, \"README.md\"), []byte(\"base\\n\"), 0o644): %v", err)
	}
	gitCmd(t, repo, "add", "README.md")
	gitCmd(t, repo, "commit", "-m", "initial")
	gitCmd(t, repo, "remote", "add", "origin", origin)
	gitCmd(t, repo, "push", "-u", "origin", "main")
	wt, err := New(repo, "main", filepath.Join(root, "worktrees"))
	if err != nil {
		t.Fatalf("New(repo, \"main\", filepath.Join(root, \"worktrees\")): %v", err)
	}
	return wt, repo
}

func TestGitWorktreeCreateUsesTaskBaseBranch(t *testing.T) {
	wt, repo := newTestGitWorktree(t)
	ctx := context.Background()

	gitCmd(t, repo, "checkout", "-b", "release/1")
	if err := os.WriteFile(filepath.Join(repo, "base-only.txt"), []byte("release\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(repo, \"base-only.txt\"), []byte(\"release\\n\"), 0o644): %v", err)
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

func TestGitWorktreeCreateRejectsCollidingTaskIDs(t *testing.T) {
	wt, _ := newTestGitWorktree(t)
	ctx := context.Background()
	first := core.Task{ID: "T-a-b"}
	ws, err := wt.Create(ctx, first)
	if err != nil {
		t.Fatalf("Create(first) error = %v", err)
	}

	// The slash is sanitized to a dash, so this id targets the same branch
	// and worktree path as the first task.
	second := core.Task{ID: "T-a/b"}
	_, err = wt.Create(ctx, second)
	if core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("Create(second) error = %v, want CodeConflict", err)
	}
	for _, id := range []string{"T-a-b", "T-a/b"} {
		if !strings.Contains(err.Error(), id) {
			t.Fatalf("Create(second) error = %q, want it to name task %s", err, id)
		}
	}
	if clean, err := wt.Clean(ctx, ws); err != nil || !clean {
		t.Fatalf("Create(second) disturbed the first worktree: clean=%v err=%v", clean, err)
	}
}

func TestGitWorktreeForceRemoveDeletesBranchWhenWorktreeIsGone(t *testing.T) {
	wt, _ := newTestGitWorktree(t)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-GONE-BRANCH"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	branch := ws.Branch

	// Drop the directory and git's administrative entry. Git then refuses
	// `worktree remove`, while the branch ref is still present locally.
	if err := os.RemoveAll(ws.Path); err != nil {
		t.Fatalf("os.RemoveAll(ws.Path): %v", err)
	}
	admin := filepath.Join(wt.Root, ".git", "worktrees", filepath.Base(ws.Path))
	if err := os.RemoveAll(admin); err != nil {
		t.Fatalf("os.RemoveAll(admin): %v", err)
	}
	if err := wt.ForceRemove(ctx, ws); err != nil {
		t.Fatalf("ForceRemove() with a missing worktree error = %v", err)
	}
	cmd := exec.Command("git", "-C", wt.Repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("stale branch %s survived ForceRemove: %s", branch, out)
	}
}

func TestGitWorktreeCreateRecoversFromDeadAdminEntry(t *testing.T) {
	wt, repo := newTestGitWorktree(t)
	ctx := context.Background()
	task := core.Task{ID: "T-DEAD-ADMIN"}
	path := filepath.Join(wt.Dir, "T-DEAD-ADMIN")
	gitCmd(t, repo, "worktree", "add", "-b", branchName(task.ID), path, "origin/main")
	if err := os.RemoveAll(path); err != nil {
		t.Fatalf("os.RemoveAll(path): %v", err)
	}
	ws, err := wt.Create(ctx, task)
	if err != nil {
		t.Fatalf("Create() after an out-of-band delete: %v", err)
	}
	if _, err := os.Stat(ws.Path); err != nil {
		t.Fatalf("worktree missing after Create(): %v", err)
	}
}

func TestGitWorktreeCreateAfterInitializingLock(t *testing.T) {
	wt, repo := newTestGitWorktree(t)
	ctx := context.Background()
	task := core.Task{ID: "T-INIT-LOCK"}
	ws, err := wt.Create(ctx, task)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// A killed add leaves the worktree locked with this exact reason. Prune
	// does not clear it, so a plain add would fail on the next dispatch.
	gitCmd(t, repo, "worktree", "lock", "--reason", "initializing", ws.Path)
	if listing := gitCmd(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(listing, "locked initializing") {
		t.Fatalf("worktree was not locked: %s", listing)
	}

	recreated, err := wt.Create(ctx, task)
	if err != nil {
		t.Fatalf("Create() after an initializing lock: %v", err)
	}
	if _, err := os.Stat(recreated.Path); err != nil {
		t.Fatalf("worktree missing after Create(): %v", err)
	}
	if listing := gitCmd(t, repo, "worktree", "list", "--porcelain"); strings.Contains(listing, "locked initializing") {
		t.Fatalf("initializing lock survived Create(): %s", listing)
	}
}

func TestGitWorktreeCreateKeepsWorktreesLockedBySomeoneElse(t *testing.T) {
	wt, repo := newTestGitWorktree(t)
	ctx := context.Background()
	task := core.Task{ID: "T-OTHER-LOCK"}
	ws, err := wt.Create(ctx, task)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	gitCmd(t, repo, "worktree", "lock", "--reason", "operator", ws.Path)

	if _, err := wt.Create(ctx, task); err == nil {
		t.Fatal("Create() removed a worktree locked with another reason")
	}
	if listing := gitCmd(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(listing, "locked operator") {
		t.Fatalf("operator lock was released: %s", listing)
	}
}

func TestGitWorktreeForceRemovePreservesUnpushedCommit(t *testing.T) {
	wt, _ := newTestGitWorktree(t)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-UNPUSHED"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, "work.txt"), []byte("worker work\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(work.txt): %v", err)
	}
	gitCmd(t, ws.Path, "add", "work.txt")
	gitCmd(t, ws.Path, "commit", "-m", "worker work")
	head := strings.TrimSpace(gitCmd(t, ws.Path, "rev-parse", "HEAD"))

	if err := wt.ForceRemove(ctx, ws); err != nil {
		t.Fatalf("ForceRemove() error = %v", err)
	}
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree survived ForceRemove(): %v", err)
	}
	remote := strings.TrimSpace(gitCmd(t, wt.Repo, "ls-remote", "origin", "refs/heads/"+ws.Branch))
	if !strings.HasPrefix(remote, head) {
		t.Fatalf("unpushed commit %s not preserved on origin: %q", head, remote)
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
		t.Fatalf("os.WriteFile(filepath.Join(repo, \"README.md\"), []byte(\"dirty main\\n\"), 0o644): %v", err)
	}
	if clean, err := wt.Clean(ctx, ws); err != nil || !clean {
		t.Fatalf("Clean() with dirty main repo = %v, %v; want true, nil", clean, err)
	}

	dirty := filepath.Join(ws.Path, "agent.txt")
	if err := os.WriteFile(dirty, []byte("agent work\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(dirty, []byte(\"agent work\\n\"), 0o644): %v", err)
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
		t.Fatalf("os.Remove(dirty): %v", err)
	}
	cardDir := filepath.Join(ws.Path, "docs", "tasks")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(cardDir, 0o755): %v", err)
	}
	card := filepath.Join(cardDir, "T-CLEAN.md")
	if err := os.WriteFile(card, []byte("status: in_progress\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(card, []byte(\"status: in_progress\\n\"), 0o644): %v", err)
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
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-REBASE-CARD\"}): %v", err)
	}
	cardDir := filepath.Join(ws.Path, "docs", "tasks")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(cardDir, 0o755): %v", err)
	}
	card := filepath.Join(cardDir, "T-NEW.md")
	if err := os.WriteFile(card, []byte("status: available\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(card, []byte(\"status: available\\n\"), 0o644): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "base2.txt"), []byte("advanced\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(repo, \"base2.txt\"), []byte(\"advanced\\n\"), 0o644): %v", err)
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
	if !strings.Contains(err.Error(), branch) {
		t.Fatalf("Create() error = %q, want it to name the branch that is in use", err)
	}
	if !strings.Contains(err.Error(), "already used by worktree") {
		t.Fatalf("Create() error = %q, want git's refusal detail", err)
	}
}

func TestGitWorktreeCreateCleanPathLimitsGitCalls(t *testing.T) {
	wt, _ := newTestGitWorktree(t)
	ctx := context.Background()
	var calls [][]string
	wt.commandHook = func(_ string, args []string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	if _, err := wt.Create(ctx, core.Task{ID: "T-COUNT"}); err != nil {
		t.Fatalf("Create(): %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("Create() ran %d git commands, want fetch and worktree add only: %v", len(calls), calls)
	}
	for _, call := range calls {
		if len(call) > 0 && call[0] == "merge-base" {
			t.Fatalf("Create() spawned merge-base on a clean path: %v", calls)
		}
		if len(call) > 1 && call[0] == "worktree" && call[1] == "prune" {
			t.Fatalf("Create() pruned on a clean path: %v", calls)
		}
		if len(call) > 1 && call[0] == "show-ref" {
			t.Fatalf("Create() probed show-ref on a clean path: %v", calls)
		}
	}
}

func TestGitWorktreeCardOnlyDirtUsesOneStatusCall(t *testing.T) {
	wt, _ := newTestGitWorktree(t)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-CARD-DIRT-COUNT"})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	cards := filepath.Join(ws.Path, "docs", "tasks")
	if err := os.MkdirAll(cards, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(cards): %v", err)
	}
	if err := os.WriteFile(filepath.Join(cards, "stale.md"), []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(stale.md): %v", err)
	}

	var statusCalls int
	wt.commandHook = func(_ string, args []string) error {
		if len(args) > 0 && args[0] == "status" {
			statusCalls++
		}
		return nil
	}
	dirty, onlyCards, err := wt.cardOnlyDirt(ctx, ws.Path)
	if err != nil {
		t.Fatalf("cardOnlyDirt(): %v", err)
	}
	if !dirty || !onlyCards {
		t.Fatalf("cardOnlyDirt() = (%v, %v), want (true, true)", dirty, onlyCards)
	}
	if statusCalls != 1 {
		t.Fatalf("cardOnlyDirt() ran %d status calls, want 1", statusCalls)
	}

	if err := os.WriteFile(filepath.Join(ws.Path, "agent.go"), []byte("agent\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(agent.go): %v", err)
	}
	statusCalls = 0
	dirty, onlyCards, err = wt.cardOnlyDirt(ctx, ws.Path)
	if err != nil {
		t.Fatalf("cardOnlyDirt() with agent code: %v", err)
	}
	if !dirty || onlyCards {
		t.Fatalf("cardOnlyDirt() with agent code = (%v, %v), want (true, false)", dirty, onlyCards)
	}
	if statusCalls != 1 {
		t.Fatalf("cardOnlyDirt() ran %d status calls with agent code, want 1", statusCalls)
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
		t.Fatalf("os.MkdirAll(cardDir, 0o755): %v", err)
	}
	if err := os.WriteFile(filepath.Join(cardDir, "T-COMMIT.md"), []byte("---\nid: T-COMMIT\nstatus: done\n---\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(cardDir, \"T-COMMIT.md\"), []byte(\"---\\nid: T-COMMIT\\nstatus: done\\n---\\n\"), 0o644): %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, "agent.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(ws.Path, \"agent.go\"), []byte(\"package main\\n\"), 0o644): %v", err)
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
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-RETRY\"}): %v", err)
	}
	cardDir := filepath.Join(ws.Path, "docs", "tasks")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(cardDir, 0o755): %v", err)
	}
	if err := os.WriteFile(filepath.Join(cardDir, "T-RETRY.md"), []byte("card\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(cardDir, \"T-RETRY.md\"), []byte(\"card\\n\"), 0o644): %v", err)
	}
	if err := wt.CommitPath(ctx, ws, "docs/tasks", "beacon: create task T-RETRY"); err != nil {
		t.Fatalf("wt.CommitPath(ctx, ws, \"docs/tasks\", \"beacon: create task T-RETRY\"): %v", err)
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
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-PUSH-CANCEL\"}): %v", err)
	}
	cardDir := filepath.Join(ws.Path, "docs", "tasks")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(cardDir, 0o755): %v", err)
	}
	if err := os.WriteFile(filepath.Join(cardDir, "T-PUSH-CANCEL.md"), []byte("card\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(cardDir, \"T-PUSH-CANCEL.md\"), []byte(\"card\\n\"), 0o644): %v", err)
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
		t.Fatalf("os.MkdirAll(hooksDir, 0o755): %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(hook), 0o755); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(hooksDir, \"pre-push\"), []byte(hook), 0o755): %v", err)
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
		t.Fatalf("os.WriteFile(release, nil, 0o644): %v", err)
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
		t.Fatalf("os.WriteFile(filepath.Join(other, \"remote.txt\"), []byte(\"remote change\\n\"), 0o644): %v", err)
	}
	gitCmd(t, other, "add", "remote.txt")
	gitCmd(t, other, "commit", "-m", "remote change")
	gitCmd(t, other, "push", "origin", "main")

	// The card is written before the rebase, as in Create. Git tolerates the
	// untracked card file since it does not overlap the rebased history.
	cardDir := filepath.Join(repo, "docs", "tasks")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(cardDir, 0o755): %v", err)
	}
	if err := os.WriteFile(filepath.Join(cardDir, "T-REBASE.md"), []byte("card\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(cardDir, \"T-REBASE.md\"), []byte(\"card\\n\"), 0o644): %v", err)
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
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-REJECT\"}): %v", err)
	}

	// Create a divergent remote branch from a second checkout.
	other := filepath.Join(root, "other-reject")
	gitCmd(t, root, "clone", origin, other)
	gitCmd(t, other, "config", "user.name", "Test User")
	gitCmd(t, other, "config", "user.email", "test@example.com")
	gitCmd(t, other, "checkout", "-b", ws.Branch, "origin/main")
	if err := os.WriteFile(filepath.Join(other, "divergent.txt"), []byte("other\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(other, \"divergent.txt\"), []byte(\"other\\n\"), 0o644): %v", err)
	}
	gitCmd(t, other, "add", "divergent.txt")
	gitCmd(t, other, "commit", "-m", "divergent change")
	gitCmd(t, other, "push", "origin", ws.Branch)

	cardDir := filepath.Join(ws.Path, "docs", "tasks")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(cardDir, 0o755): %v", err)
	}
	if err := os.WriteFile(filepath.Join(cardDir, "T-REJECT.md"), []byte("card\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(cardDir, \"T-REJECT.md\"), []byte(\"card\\n\"), 0o644): %v", err)
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
		t.Fatalf("os.WriteFile(dirty, []byte(\"uncommitted\\n\"), 0o644): %v", err)
	}
	if changed, err := wt.HasChanges(ctx, ws, "main"); err != nil || !changed {
		t.Fatalf("HasChanges() with uncommitted file = %v, %v; want true, nil", changed, err)
	}
	if err := os.Remove(dirty); err != nil {
		t.Fatalf("os.Remove(dirty): %v", err)
	}
	if changed, err := wt.HasChanges(ctx, ws, "main"); err != nil || changed {
		t.Fatalf("HasChanges() after cleanup = %v, %v; want false, nil", changed, err)
	}

	if err := os.WriteFile(filepath.Join(ws.Path, "README.md"), []byte("agent commit\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(ws.Path, \"README.md\"), []byte(\"agent commit\\n\"), 0o644): %v", err)
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
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-REFRESH\"}): %v", err)
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
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-STALE\"}): %v", err)
	}
	if err := os.RemoveAll(ws.Path); err != nil {
		t.Fatalf("os.RemoveAll(ws.Path): %v", err)
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
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-REFRESH-FAIL\"}): %v", err)
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
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-FORCE-REFRESH\"}): %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, "WIP.txt"), []byte("agent work\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(ws.Path, \"WIP.txt\"), []byte(\"agent work\\n\"), 0o644): %v", err)
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
		t.Fatalf("os.RemoveAll(admin): %v", err)
	}
}

func TestWorktreeRemoveRefreshesWhenGitCannotRemove(t *testing.T) {
	wt, rec := worktreeWithRefresher(t, 0)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-BROKEN-ADMIN"})
	if err != nil {
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-BROKEN-ADMIN\"}): %v", err)
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
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-FORCE-BROKEN\"}): %v", err)
	}
	breakGitAdmin(t, wt, ws)
	if err := wt.ForceRemove(ctx, ws); err == nil {
		t.Fatal("ForceRemove() error = nil, want an error when git metadata is missing")
	}
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("worktree refresh calls = %d, want 1 after failed force removal", got)
	}
}

func TestWorktreeCreateStaleCleanupSkipsRefresh(t *testing.T) {
	wt, rec := worktreeWithRefresher(t, 0)
	ctx := context.Background()
	task := core.Task{ID: "T-CREATE-STALE"}
	if _, err := wt.Create(ctx, task); err != nil {
		t.Fatalf("wt.Create(ctx, task): %v", err)
	}
	// A retry finds the previous worktree still on disk. Create removes it as
	// stale and re-adds the same path, so the editor never sees a gap and the
	// refresh belongs to the eventual removal instead.
	if _, err := wt.Create(ctx, task); err != nil {
		t.Fatalf("Create() retry error = %v", err)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("worktree refresh calls = %d, want 0 after stale cleanup", got)
	}
	if got := rec.reloadCount(); got != 0 {
		t.Fatalf("location reloads = %d, want 0 after stale cleanup", got)
	}
}

func TestWorktreeRemovalReloadsLiveEditorLocations(t *testing.T) {
	wt, rec := worktreeWithRefresher(t, 0)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-RELOAD"})
	if err != nil {
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-RELOAD\"}): %v", err)
	}
	if err := wt.Remove(ctx, ws); err != nil {
		t.Fatalf("wt.Remove(ctx, ws): %v", err)
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
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-NO-RELOAD\"}): %v", err)
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
		t.Fatalf("os.WriteFile(filepath.Join(repo, \"advance.txt\"), []byte(\"new base\\n\"), 0o644): %v", err)
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

// rebaseInProgress reports whether git still holds rebase state for the path.
func rebaseInProgress(t *testing.T, path string) bool {
	t.Helper()
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		out := strings.TrimSpace(gitCmd(t, path, "rev-parse", "--git-path", name))
		if !filepath.IsAbs(out) {
			out = filepath.Join(path, out)
		}
		if _, err := os.Stat(out); err == nil {
			return true
		}
	}
	return false
}

func TestGitWorktreeCreateReportsMergeBaseCleanupFailure(t *testing.T) {
	wt, _ := newTestGitWorktree(t)
	ctx := context.Background()
	// Point the ref reader at a directory with no git metadata so base
	// resolution falls back to the merge-base subprocess.
	wt.Root = t.TempDir()
	mergeErr := errors.New("injected merge-base failure")
	cleanupErr := errors.New("injected worktree remove failure")
	wt.commandHook = func(dir string, args []string) error {
		if len(args) == 0 {
			return nil
		}
		switch args[0] {
		case "merge-base":
			return mergeErr
		case "worktree":
			if len(args) > 1 && args[1] == "remove" {
				return cleanupErr
			}
		}
		return nil
	}

	_, err := wt.Create(ctx, core.Task{ID: "T-MB-CLEANUP"})
	if !errors.Is(err, mergeErr) {
		t.Fatalf("Create() error = %v, want the merge-base fallback failure", err)
	}
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("Create() error = %v, want the worktree cleanup failure", err)
	}
}

func TestGitWorktreeRebaseBaseReportsCardDirtFailure(t *testing.T) {
	wt, _ := newTestGitWorktree(t)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-DIRT-ERR"})
	if err != nil {
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-DIRT-ERR\"}): %v", err)
	}
	statusErr := errors.New("injected status failure")
	wt.commandHook = func(dir string, args []string) error {
		if len(args) > 0 && args[0] == "status" {
			return statusErr
		}
		return nil
	}

	err = wt.RebaseBase(ctx, ws, "main")
	if !errors.Is(err, statusErr) {
		t.Fatalf("RebaseBase() error = %v, want the card dirt status failure", err)
	}
}

func TestGitWorktreeRebaseBaseAbortFailureLeavesNoRebaseState(t *testing.T) {
	wt, repo := newTestGitWorktree(t)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-ABORT-FAIL"})
	if err != nil {
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-ABORT-FAIL\"}): %v", err)
	}

	// A worktree commit that conflicts with an advance on origin/main forces
	// the rebase to stop with a real rebase state for the abort to clean up.
	if err := os.WriteFile(filepath.Join(ws.Path, "README.md"), []byte("worktree change\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(ws.Path, \"README.md\"), []byte(\"worktree change\\n\"), 0o644): %v", err)
	}
	gitCmd(t, ws.Path, "add", "README.md")
	gitCmd(t, ws.Path, "commit", "-m", "worktree change")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("origin change\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(filepath.Join(repo, \"README.md\"), []byte(\"origin change\\n\"), 0o644): %v", err)
	}
	gitCmd(t, repo, "add", "README.md")
	gitCmd(t, repo, "commit", "-m", "origin change")
	gitCmd(t, repo, "push", "origin", "main")

	abortErr := errors.New("injected abort failure")
	wt.commandHook = func(dir string, args []string) error {
		if len(args) > 1 && args[0] == "rebase" && args[1] == "--abort" {
			return abortErr
		}
		return nil
	}

	err = wt.RebaseBase(ctx, ws, "main")
	if err == nil {
		t.Fatal("RebaseBase() error = nil, want the rebase conflict and abort failure")
	}
	if !errors.Is(err, abortErr) {
		t.Fatalf("RebaseBase() error = %v, want the abort failure", err)
	}
	if !strings.Contains(err.Error(), "rebase origin/main") {
		t.Fatalf("RebaseBase() error = %v, want the original rebase failure", err)
	}
	if rebaseInProgress(t, ws.Path) {
		t.Fatal("worktree left mid-rebase after a failed abort")
	}
}

func TestGitWorktreeRemoveReportsBranchDeleteFailure(t *testing.T) {
	wt, _ := newTestGitWorktree(t)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-RM-BRANCH-ERR"})
	if err != nil {
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-RM-BRANCH-ERR\"}): %v", err)
	}
	branchErr := errors.New("injected branch delete failure")
	wt.commandHook = func(dir string, args []string) error {
		if len(args) > 1 && args[0] == "branch" {
			return branchErr
		}
		return nil
	}

	err = wt.Remove(ctx, ws)
	if !errors.Is(err, branchErr) {
		t.Fatalf("Remove() error = %v, want the branch delete failure", err)
	}
}

func TestGitWorktreeForceRemoveReportsBranchDeleteFailure(t *testing.T) {
	wt, _ := newTestGitWorktree(t)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-FRM-BRANCH-ERR"})
	if err != nil {
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-FRM-BRANCH-ERR\"}): %v", err)
	}
	branchErr := errors.New("injected branch delete failure")
	wt.commandHook = func(dir string, args []string) error {
		if len(args) > 1 && args[0] == "branch" {
			return branchErr
		}
		return nil
	}

	err = wt.ForceRemove(ctx, ws)
	if !errors.Is(err, branchErr) {
		t.Fatalf("ForceRemove() error = %v, want the branch delete failure", err)
	}
}

func TestGitWorktreeRemoveCleanPathLimitsGitCalls(t *testing.T) {
	wt, _ := newTestGitWorktree(t)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-RM-COUNT"})
	if err != nil {
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-RM-COUNT\"}): %v", err)
	}
	var calls [][]string
	wt.commandHook = func(_ string, args []string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	if err := wt.Remove(ctx, ws); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("Remove() ran %d git commands, want 2: %v", len(calls), calls)
	}
	for _, call := range calls {
		if len(call) > 0 && call[0] == "status" {
			t.Fatalf("Remove() ran a separate status check on a clean path: %v", calls)
		}
	}
}

func TestGitWorktreeRemoveDirtyPathClassifiesWithOneProbe(t *testing.T) {
	wt, _ := newTestGitWorktree(t)
	ctx := context.Background()
	ws, err := wt.Create(ctx, core.Task{ID: "T-RM-DIRTY"})
	if err != nil {
		t.Fatalf("wt.Create(ctx, core.Task{ID: \"T-RM-DIRTY\"}): %v", err)
	}
	dirty := filepath.Join(ws.Path, "agent.txt")
	if err := os.WriteFile(dirty, []byte("agent work\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(dirty, ...): %v", err)
	}
	var calls [][]string
	wt.commandHook = func(_ string, args []string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	err = wt.Remove(ctx, ws)
	if core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("Remove() error = %v, want CodeConflict for a dirty worktree", err)
	}
	if len(calls) != 2 {
		t.Fatalf("Remove() ran %d git commands on a dirty path, want 2: %v", len(calls), calls)
	}
	if calls[0][0] != "worktree" || calls[1][0] != "status" {
		t.Fatalf("Remove() probe order = %v, want worktree remove then status", calls)
	}
	if _, err := os.Stat(dirty); err != nil {
		t.Fatalf("agent file removed by a rejected Remove(): %v", err)
	}
}

func TestGitWorktreeRemoveLatencyAlternating(t *testing.T) {
	if testing.Short() {
		t.Skip("wall-clock measurement")
	}
	ctx := context.Background()
	const passes = 5
	walls := make([]float64, 0, passes)
	works := make([]int, 0, passes)
	statuses := make([]int, 0, passes)
	errs := 0
	for i := 1; i <= passes; i++ {
		wt, _ := newTestGitWorktree(t)
		ws, err := wt.Create(ctx, core.Task{ID: core.TaskID(fmt.Sprintf("T-RM-LAT-%d", i))})
		if err != nil {
			t.Fatalf("Create() pass %d: %v", i, err)
		}
		calls, statusCalls := 0, 0
		wt.commandHook = func(_ string, args []string) error {
			calls++
			if len(args) > 0 && args[0] == "status" {
				statusCalls++
			}
			return nil
		}
		start := time.Now()
		err = wt.Remove(ctx, ws)
		wall := time.Since(start).Seconds()
		walls = append(walls, wall)
		works = append(works, calls)
		statuses = append(statuses, statusCalls)
		runErrs := 0
		if err != nil {
			runErrs = 1
			errs++
		}
		t.Logf("run %d: wall=%.3fs work=%d status=%d errs=%d", i, wall, calls, statusCalls, runErrs)
	}
	sort.Float64s(walls)
	sort.Ints(works)
	sort.Ints(statuses)
	fmt.Printf("worktree-remove: median=%.3fs range=%.3fs-%.3fs work=%d status=%d errs=%d limiter=git subprocess spawn and worktree metadata\n",
		walls[len(walls)/2], walls[0], walls[len(walls)-1],
		works[len(works)/2], statuses[len(statuses)/2], errs)
	// Keep the test as the harness and print the canonical row alongside the
	// human line. The status count stays in the human line only: the canonical
	// payload has a fixed field set shared by every harness.
	cache := os.Getenv("BEACON_BENCH_CACHE")
	if cache == "" {
		cache = "unset"
	}
	row := struct {
		Key      string  `json:"key"`
		MedianMS float64 `json:"median_ms"`
		MinMS    float64 `json:"min_ms"`
		MaxMS    float64 `json:"max_ms"`
		Work     int64   `json:"work"`
		Errs     int64   `json:"errs"`
		Limiter  string  `json:"limiter"`
		Cache    string  `json:"cache"`
	}{
		Key:      "worktree-remove",
		MedianMS: walls[len(walls)/2] * 1000,
		MinMS:    walls[0] * 1000,
		MaxMS:    walls[len(walls)-1] * 1000,
		Work:     int64(works[len(works)/2]),
		Errs:     int64(errs),
		Limiter:  "git subprocess spawn and worktree metadata",
		Cache:    cache,
	}
	if data, err := json.Marshal(row); err == nil {
		fmt.Printf("beacon-perf: %s\n", data)
	}
}

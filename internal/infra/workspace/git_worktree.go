// Package workspace isolates each task in a git worktree.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// GitWorktree creates and manages one git worktree per task.
type GitWorktree struct {
	Repo      string
	Root      string
	Base      string
	Dir       string
	Timeout   time.Duration
	Refresher core.WorktreeRefresher

	// commandHook is a test seam. Production leaves it nil. A non-nil hook
	// fails a chosen git subcommand by returning an error before it runs.
	commandHook func(dir string, args []string) error
}

// New builds a worktree manager for repo with worktrees under dir.
func New(repo, base, dir string) (*GitWorktree, error) {
	if !filepath.IsAbs(dir) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		dir = abs
	}
	g := &GitWorktree{Repo: repo, Base: base, Dir: dir, Timeout: 60 * time.Second}
	root, err := g.run(context.Background(), "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	g.Root = strings.TrimSpace(root)
	return g, nil
}

// unsafeBranchChars matches characters that a branch name may not contain.
var unsafeBranchChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// branchName maps a task id to its beacon branch name.
func branchName(id core.TaskID) string {
	return "beacon/" + unsafeBranchChars.ReplaceAllString(string(id), "-")
}

// run executes git in the repository with a bounded context.
func (g *GitWorktree) run(ctx context.Context, args ...string) (string, error) {
	return g.runInDir(ctx, g.Repo, args...)
}

// runInDir executes git in dir with a bounded context.
func (g *GitWorktree) runInDir(ctx context.Context, dir string, args ...string) (string, error) {
	if g.commandHook != nil {
		if err := g.commandHook(dir, args); err != nil {
			return "", fmt.Errorf("git -C %s %s (injected): %w", dir, strings.Join(args, " "), err)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, g.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git -C %s %s: %w: %s", dir, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// checkPath verifies that a workspace path exists.
func checkPath(path string) error {
	if path == "" {
		return core.Errorf(core.CodeInvalidInput, "workspace path is empty")
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return nil
}

// status reports whether the worktree at path is clean.
func (g *GitWorktree) status(ctx context.Context, path string) (bool, error) {
	out, err := g.runInDir(ctx, path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// statusCount counts dirty paths, optionally restricted to one pathspec.
func (g *GitWorktree) statusCount(ctx context.Context, path string, pathspec ...string) (int, error) {
	args := []string{"status", "--porcelain", "--untracked-files=all"}
	if len(pathspec) > 0 {
		args = append(args, "--")
		args = append(args, pathspec...)
	}
	out, err := g.runInDir(ctx, path, args...)
	if err != nil {
		return 0, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return 0, nil
	}
	return len(strings.Split(out, "\n")), nil
}

// cardOnlyDirt reports whether a workspace is dirty and whether every dirty
// path sits under docs/tasks. A Beacon status marker is safe to discard. Agent
// code outside docs/tasks is not.
func (g *GitWorktree) cardOnlyDirt(ctx context.Context, path string) (dirty, onlyCards bool, err error) {
	all, err := g.statusCount(ctx, path)
	if err != nil {
		return false, false, err
	}
	if all == 0 {
		return false, true, nil
	}
	inside, err := g.statusCount(ctx, path, "docs/tasks")
	if err != nil {
		return false, false, err
	}
	return true, all == inside, nil
}

// Create checks out a fresh worktree for task.
func (g *GitWorktree) Create(ctx context.Context, task core.Task) (core.Workspace, error) {
	branch := branchName(task.ID)
	path := filepath.Join(g.Dir, unsafeBranchChars.ReplaceAllString(string(task.ID), "-"))
	base := task.BaseBranch
	if base == "" {
		base = g.Base
	}
	if base == "" {
		base = "main"
	}
	if err := os.MkdirAll(g.Dir, 0o755); err != nil {
		return core.Workspace{}, err
	}
	if _, err := g.run(ctx, "fetch", "origin", base); err != nil {
		return core.Workspace{}, err
	}
	if _, err := os.Stat(path); err == nil {
		// A failed spawn can leave only the card status marker behind. That
		// dirt belongs to Beacon and must not block the next dispatch. Real
		// agent code still stops the create with a conflict. The stale path is
		// removed quietly; the editor refresh belongs to public removal, since
		// this path is immediately re-added below.
		dirty, onlyCards, err := g.cardOnlyDirt(ctx, path)
		if err != nil {
			return core.Workspace{}, err
		}
		if dirty && !onlyCards {
			return core.Workspace{}, core.Errorf(core.CodeConflict, "refusing to remove dirty worktree %s", path)
		}
		if err := g.removeForce(ctx, core.Workspace{Path: path}); err != nil {
			return core.Workspace{}, err
		}
	} else if !os.IsNotExist(err) {
		return core.Workspace{}, err
	}
	// The branch name is unique per task, so add -B can create or reset it in
	// one call instead of probing show-ref and deleting the branch first.
	if _, err := g.run(ctx, "worktree", "add", "-B", branch, path, "origin/"+base); err != nil {
		// A directory removed outside this process can leave a dead
		// administrative entry that blocks the add. Prune only after a failure
		// so a clean path does not pay for it on every card.
		if _, pruneErr := g.run(ctx, "worktree", "prune"); pruneErr != nil {
			slog.Warn("worktree prune failed", "error", pruneErr)
		}
		if _, retryErr := g.run(ctx, "worktree", "add", "-B", branch, path, "origin/"+base); retryErr != nil {
			return core.Workspace{}, err
		}
	}
	mergeBase, err := g.run(ctx, "merge-base", "origin/"+base, branch)
	if err != nil {
		_, cleanupErr := g.run(ctx, "worktree", "remove", path)
		g.refresh(ctx)
		return core.Workspace{}, joinCleanup(err, "remove worktree after failed merge-base", cleanupErr)
	}
	return core.Workspace{Path: path, Branch: branch, Base: strings.TrimSpace(mergeBase)}, nil
}

// EnsureBranch publishes branch from origin/base when it is missing on origin.
// Epic children fetch their base branch before the epic opens a PR, so that
// branch has to exist independently of the PR. A present branch is a no-op.
func (g *GitWorktree) EnsureBranch(ctx context.Context, branch, base string) error {
	branch = safeEpicBranch(branch)
	if base == "" {
		base = g.Base
	}
	if base == "" {
		base = "main"
	}
	out, err := g.run(ctx, "ls-remote", "--heads", "origin", branch)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), "\trefs/heads/"+branch) {
			return nil
		}
	}
	if _, err := g.run(ctx, "fetch", "origin", base); err != nil {
		return err
	}
	// Pushing the remote-tracking ref avoids a local branch that would have to
	// be rolled back, and leaves origin untouched when the push is rejected.
	if _, err := g.run(ctx, "push", "origin", "origin/"+base+":refs/heads/"+branch); err != nil {
		return err
	}
	slog.InfoContext(ctx, "workspace: created missing base branch", "branch", branch, "base", base)
	return nil
}

// safeEpicBranch keeps the beacon/ prefix and sanitizes the id the same way
// branchName does, so children fetch the ref that EnsureBranch publishes.
func safeEpicBranch(branch string) string {
	branch = strings.TrimSpace(branch)
	if !strings.HasPrefix(branch, "beacon/") {
		branch = "beacon/" + branch
	}
	return "beacon/" + unsafeBranchChars.ReplaceAllString(strings.TrimPrefix(branch, "beacon/"), "-")
}

// Clean reports whether the workspace has no uncommitted changes.
func (g *GitWorktree) Clean(ctx context.Context, ws core.Workspace) (bool, error) {
	if err := checkPath(ws.Path); err != nil {
		return false, err
	}
	return g.status(ctx, ws.Path)
}

// RebaseBase fetches origin/<base> and rebases the workspace's local branch on
// it. The ref may be the configured base branch before a card is published, or
// the workspace's own branch after a push was rejected as non-fast-forward.
// A failed rebase is aborted so the workspace is not left mid-rebase on a retry.
func (g *GitWorktree) RebaseBase(ctx context.Context, ws core.Workspace, base string) error {
	if err := checkPath(ws.Path); err != nil {
		return err
	}
	if base == "" {
		base = g.Base
	}
	if base == "" {
		base = "main"
	}
	if _, err := g.runInDir(ctx, ws.Path, "fetch", "origin", base); err != nil {
		return err
	}
	args := []string{"rebase"}
	// A card write that lost a race with a merge must not stop a rebase. The
	// stash is restored afterwards, so an uncommitted new card stays untouched.
	dirty, onlyCards, err := g.cardOnlyDirt(ctx, ws.Path)
	if err != nil {
		return err
	}
	if dirty && onlyCards {
		args = append(args, "--autostash")
	}
	args = append(args, "origin/"+base)
	if _, err := g.runInDir(ctx, ws.Path, args...); err != nil {
		return joinCleanup(err, "abort rebase", g.abortRebase(ctx, ws.Path))
	}
	return nil
}

// abortRebase returns the worktree to a non-rebase state. A failed abort can
// leave git's rebase state behind and block the next rebase, so a quit follows
// as a fallback. Every failure is reported, none is dropped.
func (g *GitWorktree) abortRebase(ctx context.Context, path string) error {
	_, abortErr := g.runInDir(ctx, path, "rebase", "--abort")
	if abortErr == nil {
		return nil
	}
	errs := []error{fmt.Errorf("rebase --abort: %w", abortErr)}
	if _, err := g.runInDir(ctx, path, "rebase", "--quit"); err != nil {
		errs = append(errs, fmt.Errorf("rebase --quit: %w", err))
	}
	return errors.Join(errs...)
}

// isNonFastForward recognizes git's stable markers for a push rejected because
// the remote ref advanced. Git reports "fetch first" when local is behind and
// "non-fast-forward" when the refs have diverged.
func isNonFastForward(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "non-fast-forward") ||
		strings.Contains(msg, "fetch first") ||
		strings.Contains(msg, "stale info")
}

// CommitPath commits only the given pathspec in the workspace and pushes the
// branch. Agent code is never staged. The push runs even when the pathspec has
// no diff, so a retry after a rejected push still publishes a commit that an
// earlier attempt already made.
func (g *GitWorktree) CommitPath(ctx context.Context, ws core.Workspace, pathspec, message string) error {
	if err := checkPath(ws.Path); err != nil {
		return err
	}
	out, err := g.runInDir(ctx, ws.Path, "status", "--porcelain", "--", pathspec)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		if _, err := g.runInDir(ctx, ws.Path, "add", "--", pathspec); err != nil {
			return err
		}
		if _, err := g.runInDir(ctx, ws.Path, "commit", "-m", message, "--", pathspec); err != nil {
			return err
		}
	}
	// The commit is durable. Cancellation after it must not leave cards
	// committed but unpublished, so finish the push with a bounded context
	// detached from the cancelled caller.
	pushCtx, cancel := detachedPushContext(ctx, g.Timeout)
	defer cancel()
	_, err = g.runInDir(pushCtx, ws.Path, "push", "origin", ws.Branch)
	if err != nil && isNonFastForward(err) {
		return &app.NonFastForwardError{Err: err}
	}
	return err
}

// detachedPushContext returns a bounded context that keeps the caller's values
// but cannot be cancelled with it. The commit is already durable when the push
// starts, so an abort arriving mid-push must not leave it unpublished. A zero
// timeout falls back to one minute.
func detachedPushContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = time.Minute
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// HasChanges reports whether the workspace has uncommitted or unpushed work.
func (g *GitWorktree) HasChanges(ctx context.Context, ws core.Workspace, base string) (bool, error) {
	if err := checkPath(ws.Path); err != nil {
		return false, err
	}
	clean, err := g.status(ctx, ws.Path)
	if err != nil {
		return false, err
	}
	if !clean {
		return true, nil
	}
	out, err := g.runInDir(ctx, ws.Path, "rev-list", "--count", "origin/"+base+"..HEAD")
	if err != nil {
		return false, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// refresh updates worktree registration through the configured refresher.
func (g *GitWorktree) refresh(ctx context.Context) {
	if g.Refresher == nil {
		return
	}
	if err := g.Refresher.RefreshWorktrees(ctx); err != nil {
		slog.Warn("worktree refresh failed", "error", err)
	}
}

// Remove deletes a clean worktree and its local branch.
func (g *GitWorktree) Remove(ctx context.Context, ws core.Workspace) error {
	if ws.Path == "" {
		return nil
	}
	// The editor can hold a stale row even when git refuses to remove the
	// worktree. Defer the refresh so a failed removal still cleans it up.
	defer g.refresh(ctx)
	// A directory removed outside this method is not dirty. It still needs the
	// editor registration refreshed, so skip the clean check but keep going.
	clean := true
	if _, err := os.Stat(ws.Path); err == nil {
		var checkErr error
		clean, checkErr = g.Clean(ctx, ws)
		if checkErr != nil {
			return checkErr
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if !clean {
		return fmt.Errorf("refusing to remove dirty worktree %s", ws.Path)
	}
	if _, err := g.run(ctx, "worktree", "remove", ws.Path); err != nil {
		return err
	}
	if ws.Branch != "" {
		if _, err := g.run(ctx, "branch", "-d", ws.Branch); err != nil {
			return fmt.Errorf("delete branch %s: %w", ws.Branch, err)
		}
	}
	return nil
}

// ForceRemove deletes a worktree and its branch without a clean check. A
// removal is the point where the editor's worktree registry must be refreshed.
func (g *GitWorktree) ForceRemove(ctx context.Context, ws core.Workspace) error {
	defer g.refresh(ctx)
	return g.removeForce(ctx, ws)
}

// removeForce deletes a worktree and its branch without a clean check or an
// editor refresh. Create uses it for a stale path it re-adds in the same call.
func (g *GitWorktree) removeForce(ctx context.Context, ws core.Workspace) error {
	_, err := g.run(ctx, "worktree", "remove", "--force", ws.Path)
	if err == nil && ws.Branch != "" {
		if _, err := g.run(ctx, "branch", "-D", ws.Branch); err != nil {
			return fmt.Errorf("delete branch %s: %w", ws.Branch, err)
		}
	}
	return err
}

// joinCleanup keeps the primary failure and appends a cleanup failure so a
// caller sees both. A nil cleanup leaves the primary error unchanged.
func joinCleanup(primary error, what string, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	return fmt.Errorf("%w; %s: %w", primary, what, cleanup)
}

// Package workspace isolates each task in a git worktree.
package workspace

import (
	"context"
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

type GitWorktree struct {
	Repo      string
	Root      string
	Base      string
	Dir       string
	Timeout   time.Duration
	Refresher core.WorktreeRefresher
}

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

var unsafeBranchChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func branchName(id core.TaskID) string {
	return "beacon/" + unsafeBranchChars.ReplaceAllString(string(id), "-")
}

func (g *GitWorktree) run(ctx context.Context, args ...string) (string, error) {
	return g.runInDir(ctx, g.Repo, args...)
}

func (g *GitWorktree) runInDir(ctx context.Context, dir string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, g.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git -C %s %s: %w: %s", dir, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func checkPath(path string) error {
	if path == "" {
		return core.Errorf(core.CodeInvalidInput, "workspace path is empty")
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return nil
}

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
	// A directory removed outside this process can leave a dead administrative
	// entry. Pruning keeps the later branch and worktree-add steps usable.
	if _, err := g.run(ctx, "worktree", "prune"); err != nil {
		slog.Warn("worktree prune failed", "error", err)
	}
	if _, err := g.run(ctx, "fetch", "origin", base); err != nil {
		return core.Workspace{}, err
	}
	if _, err := os.Stat(path); err == nil {
		// A failed spawn can leave only the card status marker behind. That
		// dirt belongs to Beacon and must not block the next dispatch. Real
		// agent code still stops the create with a conflict.
		dirty, onlyCards, err := g.cardOnlyDirt(ctx, path)
		if err != nil {
			return core.Workspace{}, err
		}
		if dirty && !onlyCards {
			return core.Workspace{}, core.Errorf(core.CodeConflict, "refusing to remove dirty worktree %s", path)
		}
		if err := g.ForceRemove(ctx, core.Workspace{Path: path}); err != nil {
			return core.Workspace{}, err
		}
	} else if !os.IsNotExist(err) {
		return core.Workspace{}, err
	}
	if _, err := g.run(ctx, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		if _, err := g.run(ctx, "branch", "-D", branch); err != nil {
			return core.Workspace{}, fmt.Errorf("delete branch %s: %w", branch, err)
		}
	}
	if _, err := g.run(ctx, "worktree", "add", "-b", branch, path, "origin/"+base); err != nil {
		return core.Workspace{}, err
	}
	mergeBase, err := g.run(ctx, "merge-base", "origin/"+base, branch)
	if err != nil {
		_, _ = g.run(ctx, "worktree", "remove", path)
		g.refresh(ctx)
		return core.Workspace{}, err
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
	if dirty, onlyCards, err := g.cardOnlyDirt(ctx, ws.Path); err == nil && dirty && onlyCards {
		args = append(args, "--autostash")
	}
	args = append(args, "origin/"+base)
	if _, err := g.runInDir(ctx, ws.Path, args...); err != nil {
		_, _ = g.runInDir(ctx, ws.Path, "rebase", "--abort")
		return err
	}
	return nil
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

// detachedPushContext keeps a live caller context and replaces a cancelled one
// with a bounded background context. A zero timeout falls back to one minute.
func detachedPushContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	if timeout <= 0 {
		timeout = time.Minute
	}
	return context.WithTimeout(context.Background(), timeout)
}

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

func (g *GitWorktree) refresh(ctx context.Context) {
	if g.Refresher == nil {
		return
	}
	if err := g.Refresher.RefreshWorktrees(ctx); err != nil {
		slog.Warn("worktree refresh failed", "error", err)
	}
}

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
		_, _ = g.run(ctx, "branch", "-d", ws.Branch)
	}
	return nil
}

func (g *GitWorktree) ForceRemove(ctx context.Context, ws core.Workspace) error {
	defer g.refresh(ctx)
	_, err := g.run(ctx, "worktree", "remove", "--force", ws.Path)
	if err == nil && ws.Branch != "" {
		_, _ = g.run(ctx, "branch", "-D", ws.Branch)
	}
	return err
}

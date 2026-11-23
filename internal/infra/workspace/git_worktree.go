// Package workspace isolates each task in a git worktree.
package workspace

import (
	"context"
	"fmt"
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
	Repo    string
	Root    string
	Base    string
	Dir     string
	Timeout time.Duration
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
		clean, err := g.Clean(ctx, core.Workspace{Path: path})
		if err != nil {
			return core.Workspace{}, err
		}
		if !clean {
			return core.Workspace{}, core.Errorf(core.CodeConflict, "refusing to remove dirty worktree at %s", path)
		}
		if _, err := g.run(ctx, "worktree", "remove", path); err != nil {
			return core.Workspace{}, err
		}
	} else if !os.IsNotExist(err) {
		return core.Workspace{}, err
	}
	if _, err := g.run(ctx, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		_, _ = g.run(ctx, "branch", "-D", branch)
	}
	if _, err := g.run(ctx, "worktree", "add", "-b", branch, path, "origin/"+base); err != nil {
		return core.Workspace{}, err
	}
	mergeBase, err := g.run(ctx, "merge-base", "origin/"+base, branch)
	if err != nil {
		_, _ = g.run(ctx, "worktree", "remove", path)
		return core.Workspace{}, err
	}
	return core.Workspace{Path: path, Branch: branch, Base: strings.TrimSpace(mergeBase)}, nil
}

func (g *GitWorktree) Clean(ctx context.Context, ws core.Workspace) (bool, error) {
	if err := checkPath(ws.Path); err != nil {
		return false, err
	}
	return g.status(ctx, ws.Path)
}

// RebaseBase fetches origin/<base> and rebases the workspace's local branch on
// it. Publishing a card calls this before committing so a stale local base does
// not make the push fail as non-fast-forward. A failed rebase is aborted so the
// workspace is not left mid-rebase on a retry.
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
	if _, err := g.runInDir(ctx, ws.Path, "rebase", "origin/"+base); err != nil {
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
	_, err = g.runInDir(ctx, ws.Path, "push", "origin", ws.Branch)
	if err != nil && isNonFastForward(err) {
		return &app.NonFastForwardError{Err: err}
	}
	return err
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

func (g *GitWorktree) Remove(ctx context.Context, ws core.Workspace) error {
	if ws.Path == "" {
		return nil
	}
	clean, err := g.Clean(ctx, ws)
	if err != nil || !clean {
		if err == nil {
			return fmt.Errorf("refusing to remove dirty worktree %s", ws.Path)
		}
		return err
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
	_, err := g.run(ctx, "worktree", "remove", "--force", ws.Path)
	if err == nil && ws.Branch != "" {
		_, _ = g.run(ctx, "branch", "-D", ws.Branch)
	}
	return err
}

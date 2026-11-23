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
	if err := os.MkdirAll(g.Dir, 0o755); err != nil {
		return core.Workspace{}, err
	}
	if _, err := g.run(ctx, "fetch", "origin", g.Base); err != nil {
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
	if _, err := g.run(ctx, "worktree", "add", "-b", branch, path, "origin/"+g.Base); err != nil {
		return core.Workspace{}, err
	}
	base, err := g.run(ctx, "merge-base", "origin/"+g.Base, branch)
	if err != nil {
		_, _ = g.run(ctx, "worktree", "remove", path)
		return core.Workspace{}, err
	}
	return core.Workspace{Path: path, Branch: branch, Base: strings.TrimSpace(base)}, nil
}

func (g *GitWorktree) Clean(ctx context.Context, ws core.Workspace) (bool, error) {
	if err := checkPath(ws.Path); err != nil {
		return false, err
	}
	return g.status(ctx, ws.Path)
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

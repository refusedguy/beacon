// Package orchestrator coordinates maintenance of pull requests that still
// hold an active Watch after another PR has been merged. It owns only the
// small ports it needs so the composition root can supply the real store,
// merger and git process runner.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/internal/domain"
)

// GitRunner runs one git command. An empty dir means the process working
// directory. Adapters return combined output so callers can parse both a
// successful and a failing command.
type GitRunner interface {
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

// OSGitRunner runs git through os/exec.
type OSGitRunner struct{}

var _ GitRunner = OSGitRunner{}

func (OSGitRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Store is the minimal orchestrator store port the rebaser needs. The store's
// ActiveWatches already excludes closed watches, so each returned watch is a
// rebase candidate.
type Store interface {
	ActiveWatches() ([]domain.Watch, error)
}

// Merger reports which PRs the composition root has already merged. The
// rebaser skips those PRs so a finished branch never receives a force push.
type Merger interface {
	IsMerged(ctx context.Context, repo string, pr int) (bool, error)
}

// ConflictError reports the paths git could not rebase. The subscriber wake
// carries this shape so an executor sees the conflicting files without anyone
// resolving them automatically.
type ConflictError struct {
	PR       int      `json:"pr"`
	WatchID  string   `json:"watchID,omitempty"`
	Files    []string `json:"files"`
	AbortErr string   `json:"abortErr,omitempty"`
}

func (e *ConflictError) Error() string {
	msg := fmt.Sprintf("orchestrator: rebase conflict in PR %d", e.PR)
	if len(e.Files) > 0 {
		msg += ": " + strings.Join(e.Files, ", ")
	}
	if e.AbortErr != "" {
		msg += "; abort failed: " + e.AbortErr
	}
	return msg
}

// Result is the observable outcome for one watch.
type Result struct {
	PR       int    `json:"pr"`
	WatchID  string `json:"watchID"`
	Branch   string `json:"branch"`
	Worktree string `json:"worktree,omitempty"`
	OldSHA   string `json:"oldSHA"`
	NewSHA   string `json:"newSHA"`
	Pushed   bool   `json:"pushed"`
	Skipped  bool   `json:"skipped"`
	Reason   string `json:"reason,omitempty"`
	Err      error  `json:"-"`
}

// Rebaser rebases active watch branches onto origin/main after an auto-merge.
type Rebaser struct {
	Git    GitRunner
	Store  Store
	Merger Merger
	// BranchForPR maps a PR number to its local branch name. The default is
	// feat/w<pr>, the naming used by the worktrees in this project.
	BranchForPR func(int) string
}

// RebaseAll rebases every active, unmerged watch branch. It returns one Result
// per watch and joins per-watch failures into the returned error, so a caller
// can use errors.As to pull a ConflictError out of that error.
func (r *Rebaser) RebaseAll(ctx context.Context) ([]Result, error) {
	if r == nil || r.Git == nil || r.Store == nil {
		return nil, fmt.Errorf("orchestrator: rebaser needs git and store")
	}
	watches, err := r.Store.ActiveWatches()
	if err != nil {
		return nil, fmt.Errorf("orchestrator: active watches: %w", err)
	}
	listing, err := r.Git.Run(ctx, "", "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("orchestrator: worktree list: %w", err)
	}
	worktrees := parseWorktrees(listing)

	results := make([]Result, 0, len(watches))
	var errs []error
	for _, w := range watches {
		res, rebaseErr := r.rebaseWatch(ctx, w, worktrees)
		results = append(results, res)
		if rebaseErr != nil {
			errs = append(errs, rebaseErr)
		}
	}
	return results, errors.Join(errs...)
}

func (r *Rebaser) rebaseWatch(ctx context.Context, w domain.Watch, worktrees map[string]string) (Result, error) {
	res := Result{PR: w.PR, WatchID: w.ID, OldSHA: w.HeadSHA, Branch: r.branchForPR(w.PR)}
	if r.Merger != nil {
		merged, err := r.Merger.IsMerged(ctx, w.Repo, w.PR)
		if err != nil {
			res.Err = fmt.Errorf("orchestrator: PR %d merge state: %w", w.PR, err)
			return res, res.Err
		}
		if merged {
			return r.skip(res, w.HeadSHA, "merged"), nil
		}
	}
	dir, ok := worktrees[res.Branch]
	if !ok {
		return r.skip(res, w.HeadSHA, "no worktree"), nil
	}
	res.Worktree = dir

	if _, err := r.Git.Run(ctx, dir, "fetch", "origin", "main"); err != nil {
		res.Err = fmt.Errorf("orchestrator: PR %d fetch: %w", w.PR, err)
		return res, res.Err
	}
	behind, err := r.behindCount(ctx, dir)
	if err != nil {
		res.Err = fmt.Errorf("orchestrator: PR %d ahead/behind: %w", w.PR, err)
		return res, res.Err
	}
	if behind == 0 {
		head, err := r.headSHA(ctx, dir)
		if err != nil {
			res.Err = fmt.Errorf("orchestrator: PR %d head: %w", w.PR, err)
			return res, res.Err
		}
		return r.skip(res, head, "up to date"), nil
	}
	if _, err := r.Git.Run(ctx, dir, "rebase", "origin/main"); err != nil {
		return r.conflict(ctx, w, res)
	}
	head, err := r.headSHA(ctx, dir)
	if err != nil {
		res.Err = fmt.Errorf("orchestrator: PR %d head: %w", w.PR, err)
		return res, res.Err
	}
	res.NewSHA = head
	if head == w.HeadSHA {
		return r.skip(res, head, "head unchanged"), nil
	}
	if _, err := r.Git.Run(ctx, dir, "push", "--force-with-lease", "origin", res.Branch); err != nil {
		res.Err = fmt.Errorf("orchestrator: PR %d force push: %w", w.PR, err)
		return res, res.Err
	}
	res.Pushed = true
	return res, nil
}

func (r *Rebaser) skip(res Result, sha, reason string) Result {
	res.Skipped = true
	res.Reason = reason
	res.NewSHA = sha
	return res
}

// behindCount reads the number of commits in origin/main that HEAD lacks. A
// zero count means the branch already contains main and needs no rebase.
func (r *Rebaser) behindCount(ctx context.Context, dir string) (int, error) {
	out, err := r.Git.Run(ctx, dir, "rev-list", "--left-right", "--count", "origin/main...HEAD")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return 0, fmt.Errorf("unexpected rev-list output %q", strings.TrimSpace(out))
	}
	behind, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, fmt.Errorf("parse behind count %q: %w", fields[0], err)
	}
	return behind, nil
}

func (r *Rebaser) headSHA(ctx context.Context, dir string) (string, error) {
	out, err := r.Git.Run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", fmt.Errorf("empty HEAD")
	}
	return sha, nil
}

// conflict never resolves the rebase. It collects unmerged paths, aborts the
// rebase and returns a ConflictError the caller can route to the subscriber.
func (r *Rebaser) conflict(ctx context.Context, w domain.Watch, res Result) (Result, error) {
	diffOut, _ := r.Git.Run(ctx, res.Worktree, "diff", "--name-only", "--diff-filter=U")
	var files []string
	for _, line := range strings.Split(diffOut, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			files = append(files, name)
		}
	}
	abortErr := ""
	if _, err := r.Git.Run(ctx, res.Worktree, "rebase", "--abort"); err != nil {
		abortErr = err.Error()
	}
	res.Err = &ConflictError{PR: w.PR, WatchID: w.ID, Files: files, AbortErr: abortErr}
	return res, res.Err
}

func (r *Rebaser) branchForPR(pr int) string {
	if r.BranchForPR != nil {
		return r.BranchForPR(pr)
	}
	return fmt.Sprintf("feat/w%d", pr)
}

// parseWorktrees maps an attached branch ref to its worktree directory using
// the porcelain format of git worktree list. Detached worktrees have no branch
// line and are skipped.
func parseWorktrees(listing string) map[string]string {
	out := make(map[string]string)
	dir := ""
	for _, line := range strings.Split(listing, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			dir = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			if dir != "" {
				branch := strings.TrimPrefix(line, "branch ")
				branch = strings.TrimPrefix(branch, "refs/heads/")
				out[branch] = dir
			}
		}
	}
	return out
}

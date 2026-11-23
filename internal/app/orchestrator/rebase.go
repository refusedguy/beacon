// Package orchestrator coordinates maintenance of pull requests that still
// hold an active Watch after another PR has been merged. It owns only the
// small ports it needs so the composition root can supply the real store,
// merger and git process runner.
package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

// GitRunner runs one git command. An empty dir means the process working
// directory. Adapters return combined output so callers can parse both a
// successful and a failing command.
type GitRunner interface {
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

// GitRunnerFunc adapts a function to GitRunner. The process boundary stays in
// the composition root: this package cannot import os/exec under ADR 0005.
type GitRunnerFunc func(ctx context.Context, dir string, args ...string) (string, error)

func (f GitRunnerFunc) Run(ctx context.Context, dir string, args ...string) (string, error) {
	return f(ctx, dir, args...)
}

// HeadRefResolver supplies the real head branch of a PR from the GraphQL PR
// snapshot (headRefName). The composition root owns the GraphQL client, so this
// package only names the port and never imports domain or infra (ADR 0005).
type HeadRefResolver interface {
	HeadRefName(ctx context.Context, repo string, pr int) (string, error)
}

// HeadRefResolverFunc adapts a function to HeadRefResolver.
type HeadRefResolverFunc func(ctx context.Context, repo string, pr int) (string, error)

func (f HeadRefResolverFunc) HeadRefName(ctx context.Context, repo string, pr int) (string, error) {
	return f(ctx, repo, pr)
}

// WatchUpdater persists the two state changes a rebase produces. The
// composition root implements it over the Beacon store: a clean rebase writes
// the new head SHA, a conflict writes verdict=rebase with the exact unmerged
// files for the subscriber wake.
type WatchUpdater interface {
	SetHeadSHA(ctx context.Context, watchID, sha string) error
	SetRebase(ctx context.Context, watchID string, files []string) error
}

// WatchUpdaterFunc adapts two functions to WatchUpdater so the composition
// root can wire store writes without declaring a named type.
type WatchUpdaterFunc struct {
	SetHeadSHAFunc func(ctx context.Context, watchID, sha string) error
	SetRebaseFunc  func(ctx context.Context, watchID string, files []string) error
}

func (f WatchUpdaterFunc) SetHeadSHA(ctx context.Context, watchID, sha string) error {
	return f.SetHeadSHAFunc(ctx, watchID, sha)
}

func (f WatchUpdaterFunc) SetRebase(ctx context.Context, watchID string, files []string) error {
	return f.SetRebaseFunc(ctx, watchID, files)
}

// RebaseStore is the minimal store port the rebaser needs. ActiveWatches
// already excludes closed Watches, so each returned Watch is a candidate.
type RebaseStore interface {
	ActiveWatches(ctx context.Context) ([]Watch, error)
}

// RebaseMerger reports which PRs the composition root has already merged. The
// rebaser skips those PRs so a finished branch never receives a force push.
type RebaseMerger interface {
	IsMerged(ctx context.Context, repo string, pr int) (bool, error)
}

// VerdictRebase is the app-layer mirror of the domain rebase verdict. The app
// layer may not import internal/domain (ADR 0005), so the wire value is named
// here instead.
const VerdictRebase = "rebase"

// mergeQueueSerializeWindow is the time after a merge during which the next
// ready PR is held so its branch can be rebased onto the new main first. Two
// PRs that are each green against an old main can break main when merged
// back-to-back; the window gives the second PR a chance to catch up.
const mergeQueueSerializeWindow = 2 * time.Minute

// conflictReasonPrefix makes the file list in a wake reason unambiguous.
const conflictReasonPrefix = "rebase conflict files: "

// ConflictError reports the paths git could not rebase. The subscriber wake
// carries the same list so an executor sees the conflicting files without
// anyone resolving them automatically.
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

// RebaseResult is the observable outcome for one Watch.
type RebaseResult struct {
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
	Store  RebaseStore
	Merger RebaseMerger
	// Updater persists the new head SHA after a clean rebase and the rebase
	// verdict with conflicting files after a failed one. Without it the Watch
	// stays stale and an agent spends tokens redoing work that already landed.
	Updater WatchUpdater
	// Waker is called only for conflicts. A successful automatic rebase never
	// wakes an agent.
	Waker Waker
	// HeadRefs reads headRefName from the GraphQL PR snapshot. When it is nil
	// or reports no name, the branch is resolved from the worktree listing.
	HeadRefs HeadRefResolver
	// AutoRebaseCleanBehind mirrors the config flag of the same name. While it
	// is false RebaseAll changes nothing: no fetch, no rebase, no push and no
	// wake. Set it from the project config to enable the automatic path.
	AutoRebaseCleanBehind bool
	// MergeQueueSerialize turns on the merge-queue serialization gate. It is
	// false by default, so an unconfigured Rebaser never holds a ready PR.
	// When on, ShouldSerialize reports whether a ready PR must be held after a
	// recent merge. The clock and the previous merge time come from the caller:
	// this package owns neither. Wiring prevMergedAt from cibridge and persisting
	// the wait_dependency/rebase verdict lands in a follow-up PR (after W11-C).
	MergeQueueSerialize bool
}

// ShouldSerialize reports whether the serialization gate must hold a ready PR.
// With MergeQueueSerialize off (the default) it never holds. With it on, a PR
// is held when another PR merged within mergeQueueSerializeWindow before now,
// so the PR can be rebased onto the new main before it merges. A caller that
// gets true marks the Watch wait_dependency/rebase and requires a rebase on the
// new main. The time inputs are supplied by the caller; a zero time means there
// is no previous merge to serialize against.
func (r *Rebaser) ShouldSerialize(prevMergedAt, now time.Time) bool {
	if r == nil || !r.MergeQueueSerialize {
		return false
	}
	if prevMergedAt.IsZero() || now.IsZero() {
		return false
	}
	return now.Sub(prevMergedAt) < mergeQueueSerializeWindow
}

// RebaseAll rebases every active, unmerged watch branch. It returns one
// RebaseResult per watch and joins per-watch failures into the returned error,
// so a caller can use errors.As to pull a ConflictError out of that error.
func (r *Rebaser) RebaseAll(ctx context.Context) ([]RebaseResult, error) {
	if r == nil || r.Git == nil || r.Store == nil {
		return nil, fmt.Errorf("orchestrator: rebaser needs git and store")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	watches, err := r.Store.ActiveWatches(ctx)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: active watches: %w", err)
	}
	listing, err := r.Git.Run(ctx, "", "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("orchestrator: worktree list: %w", err)
	}
	worktrees := parseWorktrees(listing)

	results := make([]RebaseResult, 0, len(watches))
	errs := &observe.ErrorCollector{}
	for _, w := range watches {
		res, rebaseErr := r.rebaseWatch(ctx, w, worktrees)
		results = append(results, res)
		if rebaseErr != nil {
			errs.Add(rebaseErr)
		}
	}
	return results, errs.Error()
}

func (r *Rebaser) rebaseWatch(ctx context.Context, w Watch, worktrees []worktreeInfo) (RebaseResult, error) {
	res := RebaseResult{PR: w.PR, WatchID: w.ID, OldSHA: w.HeadSHA}
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
	if !r.AutoRebaseCleanBehind {
		return r.skip(res, w.HeadSHA, "auto-rebase disabled"), nil
	}
	branch, err := r.headRefName(ctx, w, worktrees)
	if err != nil {
		res.Err = err
		return res, err
	}
	if branch == "" {
		return r.skip(res, w.HeadSHA, "no head ref"), nil
	}
	res.Branch = branch
	dir, ok := worktreeDir(worktrees, branch)
	if !ok {
		return r.skip(res, w.HeadSHA, "no worktree"), nil
	}
	res.Worktree = dir

	status, err := r.Git.Run(ctx, dir, "status", "--porcelain")
	if err != nil {
		res.Err = fmt.Errorf("orchestrator: PR %d worktree status: %w", w.PR, err)
		return res, res.Err
	}
	if strings.TrimSpace(status) != "" {
		return r.skip(res, w.HeadSHA, "dirty worktree"), nil
	}
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
	// Refuse to rewrite a branch we cannot record. Otherwise the Watch keeps
	// the old head and an agent spends tokens on a rebase that already ran.
	if r.Updater == nil {
		res.Err = fmt.Errorf("orchestrator: PR %d has no watch updater", w.PR)
		return res, res.Err
	}
	if _, err := r.Git.Run(ctx, dir, "push", "--force-with-lease", "origin", branch); err != nil {
		res.Err = fmt.Errorf("orchestrator: PR %d force push: %w", w.PR, err)
		return res, res.Err
	}
	res.Pushed = true
	if err := r.Updater.SetHeadSHA(ctx, w.ID, head); err != nil {
		res.Err = fmt.Errorf("orchestrator: PR %d update head SHA: %w", w.PR, err)
		return res, res.Err
	}
	return res, nil
}

func (r *Rebaser) skip(res RebaseResult, sha, reason string) RebaseResult {
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

// headRefName takes the branch from the GraphQL snapshot when the resolver is
// installed. Otherwise it matches the Watch head SHA against the HEAD line of
// `git worktree list --porcelain`. There is no feat/w%d fallback: a branch we
// cannot name is skipped, never guessed.
func (r *Rebaser) headRefName(ctx context.Context, w Watch, worktrees []worktreeInfo) (string, error) {
	if r.HeadRefs != nil {
		name, err := r.HeadRefs.HeadRefName(ctx, w.Repo, w.PR)
		if err != nil {
			return "", fmt.Errorf("orchestrator: PR %d head ref: %w", w.PR, err)
		}
		if name = normalizeRef(name); name != "" {
			return name, nil
		}
	}
	if w.HeadSHA == "" {
		return "", nil
	}
	for _, wt := range worktrees {
		if wt.branch == "" || wt.head == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(w.HeadSHA), strings.ToLower(wt.head)) ||
			strings.HasPrefix(strings.ToLower(wt.head), strings.ToLower(w.HeadSHA)) {
			return wt.branch, nil
		}
	}
	return "", nil
}

// conflict never resolves the rebase. It collects unmerged paths, aborts the
// rebase, writes verdict=rebase with those paths and wakes the executor. The
// same paths travel in ConflictError and in the wake reason.
func (r *Rebaser) conflict(ctx context.Context, w Watch, res RebaseResult) (RebaseResult, error) {
	files := r.unmergedFiles(ctx, res.Worktree)
	errs := &observe.ErrorCollector{}
	conflictErr := &ConflictError{PR: w.PR, WatchID: w.ID, Files: files}
	if _, err := r.Git.Run(ctx, res.Worktree, "rebase", "--abort"); err != nil {
		conflictErr.AbortErr = err.Error()
	}
	res.Err = conflictErr
	errs.Add(conflictErr)

	if r.Updater == nil {
		errs.Addf("orchestrator: PR %d has no watch updater for conflict", w.PR)
	} else if err := r.Updater.SetRebase(ctx, w.ID, files); err != nil {
		errs.Addf("orchestrator: PR %d mark rebase: %w", w.PR, err)
	}
	if r.Waker != nil {
		reason := conflictReason(files)
		var wakeErr error
		if reasonWaker, ok := r.Waker.(ReasonWaker); ok {
			wakeErr = reasonWaker.WakeWithReason(ctx, w.ID, w.Revision, VerdictRebase, reason)
		} else {
			wakeErr = r.Waker.Wake(ctx, w.ID, w.Revision, VerdictRebase)
		}
		if wakeErr != nil {
			errs.Addf("orchestrator: PR %d wake executor: %w", w.PR, wakeErr)
		}
	}
	return res, errs.Error()
}

// unmergedFiles unions the unmerged paths reported by git diff and by git
// status. The diff catches content conflicts; the status codes catch add/add
// and delete/modify conflicts that can be missed by a bare diff filter.
func (r *Rebaser) unmergedFiles(ctx context.Context, dir string) []string {
	set := make(map[string]bool)
	if out, err := r.Git.Run(ctx, dir, "diff", "--name-only", "--diff-filter=U"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if name := cleanPath(line); name != "" {
				set[name] = true
			}
		}
	}
	if out, err := r.Git.Run(ctx, dir, "status", "--porcelain"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if len(line) < 3 || !isUnmergedCode(line[0], line[1]) {
				continue
			}
			if name := cleanPath(line[2:]); name != "" {
				set[name] = true
			}
		}
	}
	files := make([]string, 0, len(set))
	for name := range set {
		files = append(files, name)
	}
	sort.Strings(files)
	return files
}

func conflictReason(files []string) string {
	if len(files) == 0 {
		return "rebase conflict; git reported no unmerged paths"
	}
	return conflictReasonPrefix + strings.Join(files, ", ")
}

func cleanPath(s string) string {
	s = strings.TrimSpace(s)
	if unquoted, err := strconv.Unquote(s); err == nil {
		s = unquoted
	}
	return strings.TrimSpace(s)
}

func isUnmergedCode(x, y byte) bool {
	return x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D')
}

func normalizeRef(ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "refs/heads/")
	return strings.TrimSpace(ref)
}

// worktreeInfo is one attached worktree parsed from `git worktree list
// --porcelain`. Detached worktrees have a HEAD but no branch and are skipped.
type worktreeInfo struct {
	dir    string
	head   string
	branch string
}

func worktreeDir(worktrees []worktreeInfo, branch string) (string, bool) {
	for _, wt := range worktrees {
		if wt.branch == branch {
			return wt.dir, true
		}
	}
	return "", false
}

// parseWorktrees maps porcelain blocks to their directory, HEAD and branch.
func parseWorktrees(listing string) []worktreeInfo {
	var out []worktreeInfo
	var cur worktreeInfo
	flush := func() {
		if cur.dir != "" && cur.branch != "" {
			out = append(out, cur)
		}
		cur = worktreeInfo{}
	}
	for _, raw := range strings.Split(listing, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur.dir = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		case strings.HasPrefix(line, "HEAD "):
			cur.head = strings.TrimSpace(strings.TrimPrefix(line, "HEAD "))
		case strings.HasPrefix(line, "branch "):
			cur.branch = normalizeRef(strings.TrimPrefix(line, "branch "))
		}
	}
	flush()
	return out
}

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
	"sync"
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

	// owners records which task holds each live worktree target. Create claims
	// the target before touching git so two ids that sanitize to the same ref
	// cannot both open a worktree. The first run's worktree stays intact when
	// a second task tries to claim it.
	mu     sync.Mutex
	owners map[core.TaskID]createTarget
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

// unsafeBranchChars matches characters a worktree path may not contain. Branch
// refs use the shared sanitizer in app so every generated ref stays identical.
var unsafeBranchChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// branchName maps a task id to its sanitized beacon branch name.
func branchName(id core.TaskID) string {
	return app.BeaconBranch(string(id), app.SafeBranch)
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

// porcelainPaths returns every path named by a porcelain v1 status line. A
// rename names both source and destination, and either one outside docs/tasks
// means the workspace holds more than a card marker.
func porcelainPaths(line string) []string {
	if len(line) < 4 {
		return nil
	}
	rest := line[3:]
	if i := strings.Index(rest, " -> "); i >= 0 {
		return []string{
			strings.Trim(strings.TrimSpace(rest[:i]), "\""),
			strings.Trim(strings.TrimSpace(rest[i+4:]), "\""),
		}
	}
	return []string{strings.Trim(strings.TrimSpace(rest), "\"")}
}

// underCards reports whether p is the docs/tasks directory or a path below it.
func underCards(p string) bool {
	return p == "docs/tasks" || strings.HasPrefix(p, "docs/tasks/")
}

// cardOnlyDirt reports whether a workspace is dirty and whether every dirty
// path sits under docs/tasks. A Beacon status marker is safe to discard. Agent
// code outside docs/tasks is not. One status call returns the paths and Go
// classifies them, instead of asking git to count them twice.
func (g *GitWorktree) cardOnlyDirt(ctx context.Context, path string) (dirty, onlyCards bool, err error) {
	out, err := g.runInDir(ctx, path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return false, false, err
	}
	// Trim only the trailing newline. TrimSpace would eat the status field's
	// leading blank and shift every path by one character.
	out = strings.TrimRight(out, "\r\n")
	if out == "" {
		return false, true, nil
	}
	onlyCards = true
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		for _, p := range porcelainPaths(line) {
			if !underCards(p) {
				onlyCards = false
				break
			}
		}
		if !onlyCards {
			break
		}
	}
	return true, onlyCards, nil
}

// createTarget names the branch, worktree path and base ref for a task.
type createTarget struct {
	branch string
	path   string
	base   string
}

// targetFor resolves the branch, path and base a task's worktree is built from.
func (g *GitWorktree) targetFor(task core.Task) createTarget {
	base := task.BaseBranch
	if base == "" {
		base = g.Base
	}
	if base == "" {
		base = "main"
	}
	return createTarget{
		branch: branchName(task.ID),
		path:   filepath.Join(g.Dir, unsafeBranchChars.ReplaceAllString(string(task.ID), "-")),
		base:   base,
	}
}

// reserveTarget claims t for id. Two ids that sanitize to the same branch or
// path cannot both own a worktree: the second caller gets a conflict that
// names both tasks instead of silently replacing the first run's worktree.
func (g *GitWorktree) reserveTarget(id core.TaskID, t createTarget) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.owners == nil {
		g.owners = make(map[core.TaskID]createTarget)
	}
	for other, owned := range g.owners {
		if other == id {
			continue
		}
		if owned.branch == t.branch || owned.path == t.path {
			return core.Errorf(core.CodeConflict,
				"task %s cannot create worktree: branch %s and path %s collide with task %s",
				id, t.branch, t.path, other)
		}
	}
	g.owners[id] = t
	return nil
}

// releaseOwner drops the claim held by id. Create calls it when a claim did
// not produce a worktree, so a retry can claim the target again.
func (g *GitWorktree) releaseOwner(id core.TaskID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.owners, id)
}

// releaseWorkspace drops the claim for the task that owns ws, matched by path
// or branch. Removal only calls it once the worktree is gone; a refused
// removal keeps the claim because the worktree still lives.
func (g *GitWorktree) releaseWorkspace(ws core.Workspace) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for id, owned := range g.owners {
		if (ws.Path != "" && owned.path == ws.Path) || (ws.Branch != "" && owned.branch == ws.Branch) {
			delete(g.owners, id)
		}
	}
}

// reclaim returns a stale worktree to a state where the next add can reuse it.
// A killed add leaves a worktree locked with the "initializing" reason, which
// no prune clears, so the lock is released first. Beacon's own card marker is
// discarded. Uncommitted agent code blocks a create but not a cancel cleanup:
// protectCode keeps that conflict. Committed work is pushed to origin before
// the branch is deleted, so no commit is lost.
//
// base names the branch whose origin ref marks pushed work. An empty base uses
// the merge base recorded in ws, then the configured base branch.
func (g *GitWorktree) reclaim(ctx context.Context, ws core.Workspace, base string, protectCode bool) error {
	if ws.Path == "" {
		return nil
	}
	if err := g.unlockInitializing(ctx, ws.Path); err != nil {
		return err
	}
	if _, err := os.Stat(ws.Path); os.IsNotExist(err) {
		if ws.Branch != "" {
			return g.deleteBranch(ctx, ws.Branch, true)
		}
		return nil
	} else if err != nil {
		return err
	}
	dirty, onlyCards, err := g.cardOnlyDirt(ctx, ws.Path)
	if err != nil {
		return err
	}
	if dirty && !onlyCards && protectCode {
		return core.WorkspaceConflict(ws.Path)
	}
	if err := g.preserveCommits(ctx, ws, base); err != nil {
		return err
	}
	return g.removeForce(ctx, ws)
}

// unlockInitializing releases only a lock left by a killed worktree add. A
// lock with another reason was set on purpose and must stay in place.
func (g *GitWorktree) unlockInitializing(ctx context.Context, path string) error {
	lock := filepath.Join(g.Root, ".git", "worktrees", filepath.Base(path), "locked")
	data, err := os.ReadFile(lock)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) != "initializing" {
		return nil
	}
	_, err = g.run(ctx, "worktree", "unlock", path)
	return err
}

// preserveCommits pushes commits that are not on origin so a later branch
// delete cannot lose them. The push uses a bounded context detached from the
// caller: once a cleanup is decided, a cancelled caller must not cut it short.
func (g *GitWorktree) preserveCommits(ctx context.Context, ws core.Workspace, base string) error {
	ref := "origin/" + base
	if base == "" {
		if ws.Base != "" {
			ref = ws.Base
		} else {
			ref = "origin/" + g.baseBranch()
		}
	}
	out, err := g.runInDir(ctx, ws.Path, "rev-list", "--count", ref+"..HEAD")
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	branch := ws.Branch
	if branch == "" {
		name, err := g.runInDir(ctx, ws.Path, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return err
		}
		branch = strings.TrimSpace(name)
	}
	if branch == "" || branch == "HEAD" {
		return core.WorkspaceConflict(ws.Path)
	}
	pushCtx, cancel := detachedPushContext(ctx, g.Timeout)
	defer cancel()
	if _, err := g.runInDir(pushCtx, ws.Path, "push", "origin", branch); err != nil {
		return fmt.Errorf("preserve unpushed commits: %w", err)
	}
	return nil
}

// baseBranch is the configured base, or main when none was set.
func (g *GitWorktree) baseBranch() string {
	if g.Base != "" {
		return g.Base
	}
	return "main"
}

// Reclaim clears ws and its branch after a run is closed. It shares the
// create-path helper: an initializing lock is released, unpushed commits are
// pushed, and the worktree is removed even when it holds uncommitted edits.
// The caller controls cancellation and refresh; Reclaim only reports failures.
func (g *GitWorktree) Reclaim(ctx context.Context, ws core.Workspace, base string) error {
	defer g.refresh(ctx)
	err := g.reclaim(ctx, ws, base, false)
	if err == nil {
		g.releaseWorkspace(ws)
	}
	return err
}

// addWorktree checks branch out at path from origin/base. The branch name is
// unique per task, so add -B can create or reset it in one call instead of
// probing show-ref and deleting the branch first. A directory removed outside
// this process can leave a dead administrative entry that blocks the add, so a
// failed add is retried once after a prune.
func (g *GitWorktree) addWorktree(ctx context.Context, t createTarget) error {
	if _, err := g.run(ctx, "worktree", "add", "-B", t.branch, t.path, "origin/"+t.base); err != nil {
		if _, pruneErr := g.run(ctx, "worktree", "prune"); pruneErr != nil {
			slog.Warn("worktree prune failed", "error", pruneErr)
		}
		if _, retryErr := g.run(ctx, "worktree", "add", "-B", t.branch, t.path, "origin/"+t.base); retryErr != nil {
			return err
		}
	}
	return nil
}

// isObjectID reports whether s is a hexadecimal git object id. Both SHA-1 and
// SHA-256 lengths are accepted so a ref read works in either repository format.
func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// refSHA resolves ref to an object id without spawning git. A fetched remote
// tracking ref is usually loose; an older or pruned one lives in packed-refs.
func (g *GitWorktree) refSHA(ref string) (string, error) {
	loose := filepath.Join(g.Root, ".git", filepath.FromSlash(ref))
	if data, err := os.ReadFile(loose); err == nil {
		if sha := strings.TrimSpace(string(data)); isObjectID(sha) {
			return sha, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	packed, err := os.ReadFile(filepath.Join(g.Root, ".git", "packed-refs"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("ref %s not found", ref)
		}
		return "", err
	}
	for _, line := range strings.Split(string(packed), "\n") {
		if line == "" || line[0] == '#' || line[0] == '^' {
			continue
		}
		sha, name, ok := strings.Cut(line, " ")
		if ok && name == ref && isObjectID(sha) {
			return sha, nil
		}
	}
	return "", fmt.Errorf("ref %s not found", ref)
}

// worktreeBase resolves the base commit of a fresh worktree. add -B pins the
// branch tip to origin/base, so the merge base of the two is that tip. Reading
// the remote tracking ref gives the same commit without a git subprocess. A
// ref the disk reader cannot resolve falls back to merge-base, and either
// failure removes the worktree so a failed create leaves no stale path.
func (g *GitWorktree) worktreeBase(ctx context.Context, t createTarget) (string, error) {
	base, err := g.refSHA("refs/remotes/origin/" + t.base)
	if err == nil {
		return base, nil
	}
	mergeBase, mergeErr := g.run(ctx, "merge-base", "origin/"+t.base, t.branch)
	if mergeErr == nil {
		return strings.TrimSpace(mergeBase), nil
	}
	_, cleanupErr := g.run(ctx, "worktree", "remove", t.path)
	g.refresh(ctx)
	return "", core.JoinWith(mergeErr, "remove worktree after failed base resolution", cleanupErr)
}

// Create checks out a fresh worktree for task.
func (g *GitWorktree) Create(ctx context.Context, task core.Task) (core.Workspace, error) {
	t := g.targetFor(task)
	if err := g.reserveTarget(task.ID, t); err != nil {
		return core.Workspace{}, err
	}
	ok := false
	defer func() {
		if !ok {
			g.releaseOwner(task.ID)
		}
	}()
	if err := os.MkdirAll(g.Dir, 0o755); err != nil {
		return core.Workspace{}, err
	}
	if _, err := g.run(ctx, "fetch", "origin", t.base); err != nil {
		return core.Workspace{}, err
	}
	// A closed run can leave a locked or dirty worktree behind. Reclaim it
	// before adding so the next dispatch is not blocked by stale state.
	if err := g.reclaim(ctx, core.Workspace{Path: t.path}, t.base, true); err != nil {
		return core.Workspace{}, err
	}
	if err := g.addWorktree(ctx, t); err != nil {
		return core.Workspace{}, err
	}
	base, err := g.worktreeBase(ctx, t)
	if err != nil {
		return core.Workspace{}, err
	}
	ok = true
	return core.Workspace{Path: t.path, Branch: t.branch, Base: base}, nil
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
	return app.BeaconBranch(strings.TrimPrefix(branch, app.BeaconBranchPrefix), app.SafeBranch)
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
		return core.JoinWith(err, "abort rebase", g.abortRebase(ctx, ws.Path))
	}
	// An autostash apply can conflict while git still exits zero. The conflict
	// leaves unmerged paths and markers in the worktree, which a later
	// CommitPath would stage and publish. Stop the caller before that happens.
	unmerged, err := g.unmergedPaths(ctx, ws.Path)
	if err != nil {
		return err
	}
	if len(unmerged) > 0 {
		conflict := core.Errorf(core.CodeConflict, "rebase left unmerged paths: %s", strings.Join(unmerged, ", "))
		return core.JoinWith(conflict, "abort rebase", g.abortRebase(ctx, ws.Path))
	}
	return nil
}

// unmergedPaths lists the distinct paths git reports with unmerged stages.
// ls-files prints one line per stage, so a path can appear up to three times.
func (g *GitWorktree) unmergedPaths(ctx context.Context, path string) ([]string, error) {
	out, err := g.runInDir(ctx, path, "ls-files", "--unmerged")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		_, name, found := strings.Cut(line, "\t")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		paths = append(paths, name)
	}
	return paths, nil
}

// abortRebase returns the worktree to a non-rebase state. A failed abort can
// leave git's rebase state behind and block the next rebase, so a quit follows
// as a fallback. A completed rebase has no state to abort or quit; an autostash
// apply conflict is that case, and a reset clears unmerged paths when present.
// Every failure is reported, none is dropped.
func (g *GitWorktree) abortRebase(ctx context.Context, path string) error {
	_, abortErr := g.runInDir(ctx, path, "rebase", "--abort")
	if abortErr == nil {
		return nil
	}
	errs := []error{fmt.Errorf("rebase --abort: %w", abortErr)}
	if _, err := g.runInDir(ctx, path, "rebase", "--quit"); err != nil {
		errs = append(errs, fmt.Errorf("rebase --quit: %w", err))
		// A completed autostash rebase has no state to abort or quit. Its
		// conflict stays in the index and worktree, so clear it when present.
		// The probe keeps an unrelated failure from discarding uncommitted
		// work; the autostash still holds the local changes for a later pop.
		unmerged, probeErr := g.unmergedPaths(ctx, path)
		if probeErr == nil && len(unmerged) > 0 {
			if _, resetErr := g.runInDir(ctx, path, "reset", "--hard", "HEAD"); resetErr != nil {
				errs = append(errs, fmt.Errorf("reset --hard: %w", resetErr))
			}
		}
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
	// The removal or cleanup is already decided when the refresh runs. A
	// cancelled caller must not skip the editor cleanup, so detach from its
	// cancellation while keeping its values.
	if err := g.Refresher.RefreshWorktrees(context.WithoutCancel(ctx)); err != nil {
		slog.Warn("worktree refresh failed", "error", err)
	}
}

// isDirtyWorktreeError recognizes git's stable marker for a worktree it
// refuses to remove because it holds uncommitted files. Both wordings appear
// across supported git versions.
func isDirtyWorktreeError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "contains modified or untracked files") ||
		strings.Contains(msg, "has modified or untracked files")
}

// Remove deletes a clean worktree and its local branch.
func (g *GitWorktree) Remove(ctx context.Context, ws core.Workspace) error {
	if ws.Path == "" {
		return nil
	}
	// The editor can hold a stale row even when git refuses to remove the
	// worktree. Defer the refresh so a failed removal still cleans it up.
	defer g.refresh(ctx)
	// Git checks cleanliness inside worktree remove and leaves a dirty worktree
	// untouched. Trying the removal first avoids a separate status call on the
	// common clean path.
	_, removeErr := g.run(ctx, "worktree", "remove", ws.Path)
	if removeErr == nil {
		g.releaseWorkspace(ws)
		if ws.Branch != "" {
			return g.deleteBranch(ctx, ws.Branch, false)
		}
		return nil
	}
	// A directory removed outside this method needs no clean check. Keep the
	// git error as the result and let the deferred refresh clear the editor.
	if _, err := os.Stat(ws.Path); os.IsNotExist(err) {
		return removeErr
	} else if err != nil {
		return err
	}
	// Git names a dirty worktree in its own error. Classify from that text so
	// the dirty path keeps its conflict error without a second status call;
	// every other failure keeps the original git error.
	if isDirtyWorktreeError(removeErr) {
		return core.WorkspaceConflict(ws.Path)
	}
	return removeErr
}

// deleteBranch deletes the local branch with -d for a safe delete or -D for a
// forced one. Both paths wrap a failure with the same message.
func (g *GitWorktree) deleteBranch(ctx context.Context, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	if _, err := g.run(ctx, "branch", flag, branch); err != nil {
		return fmt.Errorf("delete branch %s: %w", branch, err)
	}
	return nil
}

// ForceRemove deletes a worktree and its branch without a clean check. A
// removal is the point where the editor's worktree registry must be refreshed.
// Committed work is pushed before the branch goes away, so a forced cleanup
// after a failed start cannot lose it.
func (g *GitWorktree) ForceRemove(ctx context.Context, ws core.Workspace) error {
	defer g.refresh(ctx)
	err := g.reclaim(ctx, ws, "", false)
	if err == nil {
		g.releaseWorkspace(ws)
	}
	return err
}

// removeForce deletes a worktree and its branch without a clean check or an
// editor refresh. Create uses it for a stale path it re-adds in the same call.
func (g *GitWorktree) removeForce(ctx context.Context, ws core.Workspace) error {
	_, removeErr := g.run(ctx, "worktree", "remove", "--force", ws.Path)
	if removeErr != nil {
		// A directory removed outside this process needs no worktree removal.
		// Git still refuses the remove when its administrative entry is gone,
		// so the branch would survive here without an explicit delete.
		if _, statErr := os.Stat(ws.Path); !os.IsNotExist(statErr) {
			return removeErr
		}
		if ws.Branch == "" {
			return removeErr
		}
		return g.deleteBranch(ctx, ws.Branch, true)
	}
	if ws.Branch != "" {
		return g.deleteBranch(ctx, ws.Branch, true)
	}
	return nil
}

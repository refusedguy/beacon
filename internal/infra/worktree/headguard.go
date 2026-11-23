// Package worktree lists, inspects and removes git worktrees for beacon gc.
// This file keeps the Watch protection rule that survives a head change. The
// collector in gc.go turns its Watch input into anchors and calls GuardWatch,
// so a Watch whose HEAD moved after registration still protects its worktree.
package worktree

import (
	"sort"
	"strings"
)

// WatchAnchor is one live Watch that can protect a worktree. HeadSHA is the
// head recorded at registration, Branch is the pull request branch and Dir is
// a worktree path from the binding registry. A Watch may carry any subset.
type WatchAnchor struct {
	ID      string
	HeadSHA string
	Branch  string
	Dir     string
	// SessionCommits holds the local commits made in this session.
	// CheckUndo accepts only commits from this list.
	SessionCommits []string
}

// AnchorsFromHeads converts the legacy head-to-Watch map into WatchAnchors.
// A map carries only the recorded head, so branch, binding and session
// commits stay empty. Sorting the keys gives GuardWatch a stable order.
func AnchorsFromHeads(heads map[string]string) []WatchAnchor {
	if len(heads) == 0 {
		return nil
	}
	keys := make([]string, 0, len(heads))
	for sha := range heads {
		keys = append(keys, sha)
	}
	sort.Strings(keys)
	anchors := make([]WatchAnchor, 0, len(keys))
	for _, sha := range keys {
		anchors = append(anchors, WatchAnchor{ID: heads[sha], HeadSHA: sha})
	}
	return anchors
}

// HeadGuardAnswer names the Watch that protected a worktree and the rule
// that matched. Reason is one of the GuardReason constants below.
type HeadGuardAnswer struct {
	ID     string
	Reason string
}

// Reasons returned by GuardWatch. A reason travels with the Watch ID so the
// collector can explain why a worktree was kept.
const (
	GuardReasonExactHead       = "head_exact"
	GuardReasonWatchBranch     = "watch_branch"
	GuardReasonWatchBinding    = "watch_binding"
	GuardReasonAncestorHead    = "head_ancestor"
	GuardReasonHeadPredecessor = "head_predecessor"
)

// GuardWatch returns the live Watch that still protects e and the rule that
// matched. It checks the recorded head first, then the Watch branch, then a
// bound directory, then ancestry. Ancestry covers a local commit made after
// registration: the recorded head is an ancestor of the worktree HEAD, so an
// exact head match would miss it and gc would delete work that has not
// reached a PR.
// An empty answer means no live Watch protects the worktree.
func GuardWatch(run Runner, e Entry, watches []WatchAnchor) (HeadGuardAnswer, bool) {
	anchors := make([]WatchAnchor, 0, len(watches))
	for _, w := range watches {
		if strings.TrimSpace(w.ID) != "" {
			anchors = append(anchors, w)
		}
	}
	if len(anchors) == 0 {
		return HeadGuardAnswer{}, false
	}
	sort.Slice(anchors, func(i, j int) bool {
		if anchors[i].ID != anchors[j].ID {
			return anchors[i].ID < anchors[j].ID
		}
		return anchors[i].HeadSHA < anchors[j].HeadSHA
	})

	heads := make(map[string]string, len(anchors))
	for _, w := range anchors {
		if head := strings.TrimSpace(w.HeadSHA); head != "" {
			heads[head] = w.ID
		}
	}
	if id := WatchIDForHead(heads, e.Head); id != "" {
		return HeadGuardAnswer{ID: id, Reason: GuardReasonExactHead}, true
	}

	branch := strings.TrimSpace(e.Branch)
	for _, w := range anchors {
		if branch != "" && strings.TrimSpace(w.Branch) == branch {
			return HeadGuardAnswer{ID: w.ID, Reason: GuardReasonWatchBranch}, true
		}
	}

	dir := cleanDir(e.Dir)
	for _, w := range anchors {
		if bound := strings.TrimSpace(w.Dir); bound != "" && cleanDir(bound) == dir {
			return HeadGuardAnswer{ID: w.ID, Reason: GuardReasonWatchBinding}, true
		}
	}

	head := strings.TrimSpace(e.Head)
	if len(head) < 7 {
		return HeadGuardAnswer{}, false
	}
	if run == nil {
		run = CommandRunner
	}
	for _, w := range anchors {
		watchHead := strings.ToLower(strings.TrimSpace(w.HeadSHA))
		if len(watchHead) < 7 {
			continue
		}
		base, err := run(e.Dir, "merge-base", head, watchHead)
		if err != nil {
			continue
		}
		switch {
		case sameCommit(base, watchHead):
			return HeadGuardAnswer{ID: w.ID, Reason: GuardReasonAncestorHead}, true
		case sameCommit(base, head):
			// Undo moved the worktree head back to a predecessor of the
			// recorded Watch head. The Watch still protects this worktree.
			return HeadGuardAnswer{ID: w.ID, Reason: GuardReasonHeadPredecessor}, true
		}
	}
	return HeadGuardAnswer{}, false
}

// sameCommit compares a git object name with a recorded head. Git prints a
// full hash and the store may hold a short one, so either side may be a
// prefix of the other.
func sameCommit(a, b string) bool {
	a = strings.ToLower(strings.TrimSpace(a))
	b = strings.ToLower(strings.TrimSpace(b))
	if len(a) < 7 || len(b) < 7 {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// Reasons a checkpoint undo is refused. The first four mirror the aider undo
// guards. The other two keep undo on a session commit at the branch tip.
const (
	UndoReasonNotSession  = "not_session_commit"
	UndoReasonNotHead     = "not_head"
	UndoReasonMergeCommit = "merge_commit"
	UndoReasonDirty       = "dirty_worktree"
	UndoReasonPushed      = "pushed_commit"
	UndoReasonGitError    = "git_error"
)

// UndoResult is the outcome of one checkpoint undo. NewHead holds the parent
// commit after a successful undo. Reason holds an UndoReason constant when
// Allowed is false.
type UndoResult struct {
	Commit  string
	NewHead string
	Allowed bool
	Reason  string
}

// CheckUndo applies the undo guards without changing the repository. commit
// must belong to this session and be the current HEAD. The tree must be clean,
// the commit must not be a merge and no origin branch may contain it.
func CheckUndo(run Runner, dir string, anchor WatchAnchor, commit string) UndoResult {
	res := UndoResult{Commit: commit}
	commit = strings.ToLower(strings.TrimSpace(commit))
	if commit == "" || !checkpointContains(anchor.SessionCommits, commit) {
		res.Reason = UndoReasonNotSession
		return res
	}
	if run == nil {
		run = CommandRunner
	}
	headOut, err := run(dir, "rev-parse", "HEAD")
	if err != nil {
		res.Reason = UndoReasonGitError
		return res
	}
	if !sameCommit(firstLine(headOut), commit) {
		res.Reason = UndoReasonNotHead
		return res
	}
	parentsOut, err := run(dir, "rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		res.Reason = UndoReasonGitError
		return res
	}
	if len(strings.Fields(firstLine(parentsOut))) > 2 {
		res.Reason = UndoReasonMergeCommit
		return res
	}
	status, err := Status(run, dir)
	if err != nil {
		res.Reason = UndoReasonGitError
		return res
	}
	if strings.TrimSpace(status) != "" {
		res.Reason = UndoReasonDirty
		return res
	}
	branches, err := run(dir, "branch", "--remotes", "--contains", commit)
	if err != nil {
		res.Reason = UndoReasonGitError
		return res
	}
	if strings.Contains(branches, "origin/") {
		res.Reason = UndoReasonPushed
		return res
	}
	res.Allowed = true
	return res
}

func firstLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

func checkpointContains(list []string, sha string) bool {
	sha = strings.ToLower(strings.TrimSpace(sha))
	for _, h := range list {
		if sameCommit(h, sha) {
			return true
		}
	}
	return false
}

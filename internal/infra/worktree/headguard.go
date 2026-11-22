// Package worktree lists, inspects and removes git worktrees for beacon gc.
// This file keeps the Watch protection rule that survives a head change. The
// collector in gc.go still calls WatchIDForHead directly; wiring this guard
// into collection belongs to the card that owns gc.go.
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
	GuardReasonExactHead    = "head_exact"
	GuardReasonWatchBranch  = "watch_branch"
	GuardReasonWatchBinding = "watch_binding"
	GuardReasonAncestorHead = "head_ancestor"
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
		if err == nil && sameCommit(base, watchHead) {
			return HeadGuardAnswer{ID: w.ID, Reason: GuardReasonAncestorHead}, true
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

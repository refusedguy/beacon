// Package cibridge owns the control-plane ports for Watch lifecycle. This file
// holds the pure filter that limits a post-merge worktree collection to the
// worktrees of the Watch whose merge triggered the pass. It does not shell out
// to git and does not import the worktree adapter (ADR 0005), so the layer
// matrix stays unchanged and the rule can be tested without a repository.
package cibridge

// WorktreeCandidate is one worktree offered to the GC after a Watch closes.
// WatchID is the live Watch whose HeadSHA matches the worktree HEAD; it is
// empty when no Watch backs the worktree. Dir is the worktree directory and
// Head is the checked-out commit, kept for reporting and head matching at the
// caller. The caller resolves WatchID before calling ScopeWorktrees.
type WorktreeCandidate struct {
	Dir     string
	Head    string
	WatchID string
}

// KeptWorktree is a candidate left out of collection with a stable reason.
type KeptWorktree struct {
	Dir    string
	Reason string
}

// ScopeResult is the split of one post-merge GC pass. Remove holds only the
// worktrees of the closed Watch; Keep explains every survivor.
type ScopeResult struct {
	Remove []WorktreeCandidate
	Keep   []KeptWorktree
}

// Reasons recorded for a worktree that the scope filter kept.
const (
	ScopeReasonForeignWatch  = "foreign_watch"
	ScopeReasonNoWatch       = "no_watch"
	ScopeReasonNoClosedWatch = "no_closed_watch"
)

// ScopeWorktrees keeps only candidates owned by closedWatchID. A candidate
// with an empty WatchID or a different WatchID stays in Keep, so a merge of
// one Watch cannot delete a clean worktree that belongs to another Watch or
// to no Watch at all. An empty closedWatchID removes nothing: without a
// closed Watch there is no scope to narrow to, and the safe answer is to keep
// every candidate.
//
// The caller uses Keep as the protected-directory set for worktree.Collect and
// Remove as the only directories the pass may collect. AUD-04 owns the port
// binding in cmd/beacon/mcp.go and wires this filter into that adapter.
func ScopeWorktrees(closedWatchID string, candidates []WorktreeCandidate) ScopeResult {
	var out ScopeResult
	for _, c := range candidates {
		switch {
		case closedWatchID == "":
			out.Keep = append(out.Keep, KeptWorktree{Dir: c.Dir, Reason: ScopeReasonNoClosedWatch})
		case c.WatchID == closedWatchID:
			out.Remove = append(out.Remove, c)
		case c.WatchID == "":
			out.Keep = append(out.Keep, KeptWorktree{Dir: c.Dir, Reason: ScopeReasonNoWatch})
		default:
			out.Keep = append(out.Keep, KeptWorktree{Dir: c.Dir, Reason: ScopeReasonForeignWatch})
		}
	}
	return out
}

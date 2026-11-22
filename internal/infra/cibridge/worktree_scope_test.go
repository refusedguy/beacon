package cibridge

import "testing"

// TestScopeWorktreesKeepsForeignCleanWorktree pins the regression from the WG-01
// research: one merged Watch must not remove a clean worktree of another Watch
// or of no Watch. The second candidate is clean and has no Watch, so it has to
// survive in result.Keep while the closed Watch's worktree is the only one
// offered for removal.
func TestScopeWorktreesKeepsForeignCleanWorktree(t *testing.T) {
	const closedID = "watch:refusedguy/beacon:7:aaa1111"
	candidates := []WorktreeCandidate{
		{Dir: "/worktrees/closed", Head: "aaa1111", WatchID: closedID},
		{Dir: "/worktrees/foreign", Head: "bbb2222"}, // clean, no Watch
	}

	got := ScopeWorktrees(closedID, candidates)

	if len(got.Remove) != 1 {
		t.Fatalf("Remove len = %d, want 1: %+v", len(got.Remove), got.Remove)
	}
	if got.Remove[0].Dir != "/worktrees/closed" {
		t.Fatalf("Remove[0].Dir = %q, want /worktrees/closed", got.Remove[0].Dir)
	}
	if len(got.Keep) != 1 {
		t.Fatalf("Keep len = %d, want 1: %+v", len(got.Keep), got.Keep)
	}
	kept := got.Keep[0]
	if kept.Dir != "/worktrees/foreign" {
		t.Fatalf("Keep[0].Dir = %q, want /worktrees/foreign", kept.Dir)
	}
	if kept.Reason != ScopeReasonNoWatch {
		t.Fatalf("Keep[0].Reason = %q, want %q", kept.Reason, ScopeReasonNoWatch)
	}
}

// TestScopeWorktreesKeepsOtherWatch proves that a clean worktree backed by a
// different live Watch is kept with its own reason, not silently dropped.
func TestScopeWorktreesKeepsOtherWatch(t *testing.T) {
	const closedID = "watch:closed"
	candidates := []WorktreeCandidate{
		{Dir: "/worktrees/closed", WatchID: closedID},
		{Dir: "/worktrees/other", WatchID: "watch:other"},
	}

	got := ScopeWorktrees(closedID, candidates)

	if len(got.Remove) != 1 || got.Remove[0].Dir != "/worktrees/closed" {
		t.Fatalf("Remove = %+v, want only /worktrees/closed", got.Remove)
	}
	if len(got.Keep) != 1 {
		t.Fatalf("Keep = %+v, want one entry", got.Keep)
	}
	if got.Keep[0].Dir != "/worktrees/other" || got.Keep[0].Reason != ScopeReasonForeignWatch {
		t.Fatalf("Keep[0] = %+v, want /worktrees/other with %q", got.Keep[0], ScopeReasonForeignWatch)
	}
}

// TestScopeWorktreesWithoutClosedWatchRemovesNothing covers the defensive
// case: the port was called without a Watch ID, so no directory is in scope.
func TestScopeWorktreesWithoutClosedWatchRemovesNothing(t *testing.T) {
	candidates := []WorktreeCandidate{
		{Dir: "/worktrees/a", WatchID: "watch:a"},
		{Dir: "/worktrees/b"},
	}

	got := ScopeWorktrees("", candidates)

	if len(got.Remove) != 0 {
		t.Fatalf("Remove = %+v, want empty", got.Remove)
	}
	if len(got.Keep) != len(candidates) {
		t.Fatalf("Keep len = %d, want %d", len(got.Keep), len(candidates))
	}
	for _, kept := range got.Keep {
		if kept.Reason != ScopeReasonNoClosedWatch {
			t.Errorf("Keep dir %q reason = %q, want %q", kept.Dir, kept.Reason, ScopeReasonNoClosedWatch)
		}
	}
}

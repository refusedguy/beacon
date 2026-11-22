package session

import (
	"reflect"
	"testing"
	"time"
)

func TestCollectBranchCandidatesRemoteOnly(t *testing.T) {
	got := CollectBranchCandidates(nil, []string{"origin/feature/resume"}, nil)

	want := []BranchCandidate{{
		Name:   "feature/resume",
		Remote: true,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
}

func TestCollectBranchCandidatesWorktreeKeepsPathAndDirty(t *testing.T) {
	at := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	got := CollectBranchCandidates(
		[]string{"fix/cd09"},
		[]string{"origin/fix/cd09"},
		[]Worktree{{Branch: "fix/cd09", Path: "/worktrees/cd09", Dirty: true, Modified: at}},
	)

	if len(got) != 1 {
		t.Fatalf("candidates = %d, want 1", len(got))
	}
	c := got[0]
	if c.Name != "fix/cd09" {
		t.Errorf("name = %q, want fix/cd09", c.Name)
	}
	if c.Worktree != "/worktrees/cd09" || !c.Dirty {
		t.Errorf("worktree = %q, dirty = %v", c.Worktree, c.Dirty)
	}
	if c.Remote {
		t.Error("remote = true for a branch with a local name and a worktree")
	}
	if !c.Modified.Equal(at) {
		t.Errorf("modified = %v, want %v", c.Modified, at)
	}
}

func TestCollectBranchCandidatesSortsByModifiedThenName(t *testing.T) {
	older := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	worktrees := []Worktree{
		{Branch: "branch-old", Path: "/wt/old", Modified: older},
		{Branch: "branch-new", Path: "/wt/new", Modified: newer},
	}
	got := CollectBranchCandidates(nil, []string{"origin/zeta", "origin/alpha"}, worktrees)

	names := make([]string, 0, len(got))
	for _, c := range got {
		names = append(names, c.Name)
	}
	want := []string{"branch-new", "branch-old", "alpha", "zeta"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("order = %v, want %v", names, want)
	}
}

func TestCollectBranchCandidatesLocalBranchIsNotRemote(t *testing.T) {
	got := CollectBranchCandidates([]string{"feature/x"}, []string{"origin/feature/x"}, nil)

	if len(got) != 1 {
		t.Fatalf("candidates = %d, want 1", len(got))
	}
	if got[0].Remote {
		t.Error("remote = true for a branch that exists locally")
	}
}

func TestCollectBranchCandidatesDoesNotMutateInputs(t *testing.T) {
	local := []string{"local-a", "local-b"}
	remote := []string{"origin/remote-a"}
	at := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	worktrees := []Worktree{{Branch: "local-a", Path: "/wt/a", Modified: at}}

	localCopy := append([]string(nil), local...)
	remoteCopy := append([]string(nil), remote...)
	worktreeCopy := append([]Worktree(nil), worktrees...)

	first := CollectBranchCandidates(local, remote, worktrees)
	second := CollectBranchCandidates(local, remote, worktrees)

	if !reflect.DeepEqual(local, localCopy) || !reflect.DeepEqual(remote, remoteCopy) || !reflect.DeepEqual(worktrees, worktreeCopy) {
		t.Fatalf("inputs changed: local=%v remote=%v worktrees=%v", local, remote, worktrees)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("second call = %#v, want %#v", second, first)
	}
}

func TestCollectBranchCandidatesEmptyInputs(t *testing.T) {
	got := CollectBranchCandidates(nil, nil, nil)
	if len(got) != 0 {
		t.Fatalf("candidates = %#v, want none", got)
	}
}

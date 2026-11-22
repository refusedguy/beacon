// Package session holds the pure logic for finding resumable work. Finding
// candidate branches stays free of git calls: the caller supplies the lists it
// already collected, and this file only merges, marks and sorts them.
package session

import (
	"sort"
	"strings"
	"time"
)

// Worktree is one worktree the boundary owner has already read from disk. It
// attaches to a branch by name and carries the path, the last modification time
// used for sorting, and whether the tree has uncommitted changes. It is a
// primitive copy so the app layer names no infra type.
type Worktree struct {
	Branch   string
	Path     string
	Dirty    bool
	Modified time.Time
}

// BranchCandidate is one branch the caller can resume. Remote is set only when
// the branch lives on the remote and has no local branch and no worktree.
// Worktree and Dirty describe a branch that is checked out in a worktree.
type BranchCandidate struct {
	Name     string
	Remote   bool
	Worktree string
	Dirty    bool
	Modified time.Time
}

// CollectBranchCandidates merges local branch names, remote branch names and
// worktrees into one list of resumable candidates. It calls no git command and
// creates no directory: the boundary owner runs fetch --prune, lists branches
// and reads worktrees before calling it. Remote names may carry an "origin/"
// prefix, which is removed so they match the local name.
//
// A branch is emitted once. A worktree keeps its path and dirty flag on the
// candidate. Remote is true only for a branch that has no local name and no
// worktree. Candidates are sorted by worktree modification time descending,
// then by name ascending; a candidate without a worktree has a zero time and
// sorts with the others by name. The inputs are never modified, so calling
// twice with the same lists yields the same result.
func CollectBranchCandidates(local, remote []string, worktrees []Worktree) []BranchCandidate {
	byName := make(map[string]*BranchCandidate)
	localNames := make(map[string]bool)

	ensure := func(name string) *BranchCandidate {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil
		}
		if c, ok := byName[name]; ok {
			return c
		}
		c := &BranchCandidate{Name: name}
		byName[name] = c
		return c
	}

	for _, name := range local {
		c := ensure(name)
		if c == nil {
			continue
		}
		localNames[c.Name] = true
		c.Remote = false
	}
	for _, name := range remote {
		c := ensure(remoteBranchName(name))
		if c != nil && !localNames[c.Name] {
			c.Remote = true
		}
	}
	for _, wt := range worktrees {
		c := ensure(wt.Branch)
		if c == nil {
			continue
		}
		c.Worktree = wt.Path
		c.Dirty = wt.Dirty
		c.Modified = wt.Modified
		c.Remote = false
	}

	out := make([]BranchCandidate, 0, len(byName))
	for _, c := range byName {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Modified.Equal(out[j].Modified) {
			return out[i].Modified.After(out[j].Modified)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// remoteBranchName strips the conventional "origin/" prefix so a remote branch
// matches its local counterpart. A name without the prefix is left alone.
func remoteBranchName(name string) string {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "origin/") {
		return strings.TrimPrefix(name, "origin/")
	}
	return name
}

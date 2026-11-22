// Package session holds pure helpers for worktree naming. The functions in
// this file take the state a caller has already collected. They call no git
// command and read no directory, so a caller can test the naming rules alone.
package session

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

const (
	// SlugHashLength is the number of hex characters in a collision suffix.
	SlugHashLength = 6
	// WorktreeCreate tells the caller to add a new worktree.
	WorktreeCreate = "create"
	// WorktreeReuse tells the caller to open the checkout it already has.
	WorktreeReuse = "reuse"

	defaultWorktreeSlug = "worktree"
)

// Worktree is one checkout the caller has discovered. Name is the worktree
// name or path. Branch is the branch checked out there.
type Worktree struct {
	Name   string
	Branch string
}

// WorktreePlan is the naming decision for one branch. Name is the worktree to
// create or reuse. Action is WorktreeCreate or WorktreeReuse.
type WorktreePlan struct {
	Name   string
	Action string
}

// SlugHash returns the hex suffix for a branch and an attempt number. The
// value depends only on its two arguments, so the order in which a caller
// walks branches or directories cannot change it.
func SlugHash(branch string, attempt int) string {
	sum := sha1.Sum([]byte(branch + ":" + strconv.Itoa(attempt)))
	return hex.EncodeToString(sum[:])[:SlugHashLength]
}

// BranchSlug returns a filesystem-safe slug for branch. It takes the last
// path part of the branch and keeps letters, digits and single dashes. When
// that name is already in taken, it appends SlugHash for the first free
// attempt. A repeat with the same inputs returns the same slug.
func BranchSlug(branch string, taken []string) string {
	base := slugBase(branch)
	if !nameTaken(taken, base) {
		return base
	}
	for attempt := 1; ; attempt++ {
		candidate := base + "-" + SlugHash(branch, attempt)
		if !nameTaken(taken, candidate) {
			return candidate
		}
	}
}

// PlanWorktree resolves branch against occupied names and existing checkouts.
// A checkout already on branch wins, so a retry reuses it instead of creating
// a second copy. When several checkouts hold the branch, the smallest name
// wins and the result does not depend on the input order. Every other
// checkout name joins taken before the new slug is chosen.
func PlanWorktree(branch string, taken []string, existing []Worktree) WorktreePlan {
	branch = strings.TrimSpace(branch)
	occupied := append([]string(nil), taken...)
	var reusable []string

	for _, wt := range existing {
		name := strings.TrimSpace(wt.Name)
		if name == "" {
			continue
		}
		if sameBranch(wt.Branch, branch) {
			reusable = append(reusable, name)
			continue
		}
		occupied = append(occupied, name)
	}

	if len(reusable) > 0 {
		sort.Strings(reusable)
		return WorktreePlan{Name: reusable[0], Action: WorktreeReuse}
	}
	return WorktreePlan{Name: BranchSlug(branch, occupied), Action: WorktreeCreate}
}

func slugBase(branch string) string {
	part := strings.TrimSpace(branch)
	if i := strings.LastIndex(part, "/"); i >= 0 {
		part = part[i+1:]
	}
	var out []byte
	for _, r := range strings.ToLower(part) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, byte(r))
		case len(out) > 0 && out[len(out)-1] != '-':
			out = append(out, '-')
		}
	}
	slug := strings.Trim(string(out), "-")
	if slug == "" {
		return defaultWorktreeSlug
	}
	return slug
}

func nameTaken(taken []string, name string) bool {
	for _, item := range taken {
		if strings.TrimSpace(item) == name {
			return true
		}
	}
	return false
}

func sameBranch(left, right string) bool {
	left = strings.TrimPrefix(strings.TrimSpace(left), "refs/heads/")
	right = strings.TrimPrefix(strings.TrimSpace(right), "refs/heads/")
	return left != "" && left == right
}

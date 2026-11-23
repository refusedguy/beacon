// Package orchestrator coordinates maintenance of pull requests that still
// hold an active Watch after another PR has been merged. This file holds the
// pre-merge conflict check: it reads git output and reports blockers without
// ever writing the index or the working tree.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// DryRunResult is the observable output of a pre-merge conflict check. Files
// lists paths whose merge output carried conflict markers. Changed lists paths
// from the read-only divergence fallback. Both stay empty for a clean merge.
// The rebase verdict and the wake reason keep using ConflictError.Files, so
// this type is additive and does not change that contract.
type DryRunResult struct {
	Files    []string `json:"files"`
	Changed  []string `json:"changed,omitempty"`
	Diverged bool     `json:"diverged,omitempty"`
}

// Conflict reports whether the check found anything that would block a clean
// merge or rebase.
func (r DryRunResult) Conflict() bool {
	return len(r.Files) > 0 || len(r.Changed) > 0
}

// ParseMergeTreeConflicts is a pure parser for git merge-tree output. It keeps
// the most recent diff header and records that path when a conflict marker is
// seen. A marker with no preceding file name is ignored, so stray markers in
// informational messages never produce a phantom path.
func ParseMergeTreeConflicts(output string) []string {
	var files []string
	seen := make(map[string]bool)
	current := ""
	for _, line := range strings.Split(output, "\n") {
		if name, ok := mergeTreeFilePath(line); ok {
			current = name
			continue
		}
		if !strings.Contains(line, "<<<<<<<") && !strings.Contains(line, ">>>>>>>") {
			continue
		}
		if current == "" || seen[current] {
			continue
		}
		seen[current] = true
		files = append(files, current)
	}
	return files
}

// mergeTreeFilePath returns the path from a "diff --git a/x b/x" or a
// "diff --cc x" header. Only the common two forms git prints are handled;
// quotes and the a/ b/ prefixes are removed so the result is a plain path.
func mergeTreeFilePath(line string) (string, bool) {
	line = strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(line, "diff --git "):
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return "", false
		}
		return stripDiffPrefix(fields[len(fields)-1]), true
	case strings.HasPrefix(line, "diff --cc "):
		path := strings.TrimSpace(strings.TrimPrefix(line, "diff --cc "))
		return cleanPath(path), path != ""
	default:
		return "", false
	}
}

func stripDiffPrefix(path string) string {
	path = cleanPath(path)
	if strings.HasPrefix(path, "b/") {
		path = strings.TrimPrefix(path, "b/")
	}
	return path
}

// FallbackChangedFiles is the pure fallback used when git merge-tree is not
// available. It unions the two changed-file lists only when both sides moved,
// so a fast-forward or a one-sided change never reports a conflict. The caller
// supplies the lists from a read-only git diff; this function never runs a
// command.
func FallbackChangedFiles(behind, ahead int, ours, theirs []string) DryRunResult {
	if behind <= 0 || ahead <= 0 {
		return DryRunResult{}
	}
	set := make(map[string]bool)
	for _, name := range ours {
		if n := cleanPath(name); n != "" {
			set[n] = true
		}
	}
	for _, name := range theirs {
		if n := cleanPath(name); n != "" {
			set[n] = true
		}
	}
	changed := slices.Sorted(maps.Keys(set))
	return DryRunResult{Changed: changed, Diverged: true}
}

// DryRunGit is the process boundary for the pre-merge conflict check. It owns
// no state and runs only read-only git commands: merge-tree, rev-list and
// diff --name-only. Nothing in this file rebases, checks out, resets or
// otherwise changes the working tree.
type DryRunGit struct {
	Git GitRunner
}

// Check reports conflicts for merging theirs into ours using base as the
// common ancestor. It returns before any rebase touches the worktree, so a
// caller can wake the subscriber with the file list while the tree is still
// clean. When merge-tree is unavailable it falls back to a read-only rev-list
// and diff comparison.
func (d *DryRunGit) Check(ctx context.Context, dir, base, ours, theirs string) (DryRunResult, error) {
	if d == nil || d.Git == nil {
		return DryRunResult{}, errors.New("orchestrator: dry-run has no git runner")
	}
	out, err := d.Git.Run(ctx, dir, "merge-tree", base, ours, theirs)
	if err == nil {
		return DryRunResult{Files: ParseMergeTreeConflicts(out)}, nil
	}

	// merge-tree may be missing on older git. Compare the two sides read-only.
	counts, err := d.Git.Run(ctx, dir, "rev-list", "--left-right", "--count", ours+"..."+theirs)
	if err != nil {
		return DryRunResult{}, err
	}
	behind, ahead, ok := parseBehindAhead(counts)
	if !ok {
		return DryRunResult{}, fmt.Errorf("orchestrator: bad rev-list counts %q", counts)
	}
	oursOut, err := d.Git.Run(ctx, dir, "diff", "--name-only", base, ours)
	if err != nil {
		return DryRunResult{}, err
	}
	theirsOut, err := d.Git.Run(ctx, dir, "diff", "--name-only", base, theirs)
	if err != nil {
		return DryRunResult{}, err
	}
	return FallbackChangedFiles(behind, ahead, splitNonEmptyLines(oursOut), splitNonEmptyLines(theirsOut)), nil
}

// parseBehindAhead reads the two counts from "rev-list --left-right --count
// ours...theirs": left is commits only on ours (ahead), right is commits only
// on theirs (behind).
func parseBehindAhead(out string) (behind, ahead int, ok bool) {
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, false
	}
	ahead, errA := strconv.Atoi(fields[0])
	behind, errB := strconv.Atoi(fields[1])
	if errA != nil || errB != nil {
		return 0, 0, false
	}
	return behind, ahead, true
}

func splitNonEmptyLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

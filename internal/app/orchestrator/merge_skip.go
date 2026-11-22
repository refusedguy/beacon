// This file classifies merge queue elements before they reach the merge API.
// The classifier is pure: callers supply problem codes, and nothing here reads
// the store or calls GitHub.
package orchestrator

import (
	"sort"
	"strings"
)

// Problem codes from CD-15. Callers pass the codes they already collected for
// one queue element; the classifier never runs git itself.
const (
	SkipProblemMainDirty           = "main_dirty"
	SkipProblemWorktreeUncommitted = "worktree_uncommitted"
	SkipProblemMergeConflict       = "merge_conflict"
	SkipProblemNothingToMerge      = "nothing_to_merge"
)

// MergeSkipDecision is the action the merge queue takes for one element.
type MergeSkipDecision string

const (
	MergeSkipMerge MergeSkipDecision = "merge"
	MergeSkipSkip  MergeSkipDecision = "skip"
	MergeSkipHold  MergeSkipDecision = "hold"
)

// MergeSkipResult holds the decision for one element and the reason a report
// shows. A skip reason is the code that made the element empty, so a report
// never confuses a skip with a hold.
type MergeSkipResult struct {
	Decision MergeSkipDecision
	Reason   string
}

// ClassifyMergeSkip decides what to do with one queue element.
//
// An element with no problems merges. An element whose only problem is
// nothing_to_merge is skipped: it has no commits to merge and must not take a
// serialization slot. Every other element is held, including a branch that is
// empty and dirty at once, because the dirty state still needs an executor.
// Unknown codes are held as well, so an unrecognized problem never lets a
// merge through. Duplicate and blank codes are ignored.
func ClassifyMergeSkip(problems []string) MergeSkipResult {
	codes := normalizeSkipProblems(problems)
	if len(codes) == 0 {
		return MergeSkipResult{Decision: MergeSkipMerge, Reason: "ready"}
	}
	if len(codes) == 1 && codes[0] == SkipProblemNothingToMerge {
		return MergeSkipResult{Decision: MergeSkipSkip, Reason: SkipProblemNothingToMerge}
	}
	return MergeSkipResult{Decision: MergeSkipHold, Reason: strings.Join(codes, "+")}
}

// normalizeSkipProblems trims codes, drops blanks, removes duplicates, and
// sorts the rest. A stable order keeps reasons and reports reproducible.
func normalizeSkipProblems(problems []string) []string {
	seen := make(map[string]bool, len(problems))
	codes := make([]string, 0, len(problems))
	for _, problem := range problems {
		code := strings.TrimSpace(problem)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// MergeSkipItem is one queue element with the problem codes already gathered
// for it. ID identifies the element in a report; the classifier never uses it
// to look anything up.
type MergeSkipItem struct {
	ID       string
	Problems []string
}

// MergeSkipReport partitions queue elements by decision. Skipped and held
// elements stay in separate lists, so a report counts skips apart from holds.
type MergeSkipReport struct {
	Merge   []MergeSkipItem
	Skipped []MergeSkipItem
	Held    []MergeSkipItem
}

// BuildMergeSkipReport classifies every element once and returns the merge
// call list next to the skip and hold lists. The caller sends only Merge to
// the merge API.
func BuildMergeSkipReport(items []MergeSkipItem) MergeSkipReport {
	report := MergeSkipReport{}
	for _, item := range items {
		switch ClassifyMergeSkip(item.Problems).Decision {
		case MergeSkipMerge:
			report.Merge = append(report.Merge, item)
		case MergeSkipSkip:
			report.Skipped = append(report.Skipped, item)
		default:
			report.Held = append(report.Held, item)
		}
	}
	return report
}

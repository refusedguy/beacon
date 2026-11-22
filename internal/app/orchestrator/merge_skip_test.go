package orchestrator

import (
	"strings"
	"testing"
)

func TestClassifyMergeSkipEmptyBranch(t *testing.T) {
	got := ClassifyMergeSkip([]string{SkipProblemNothingToMerge})
	if got.Decision != MergeSkipSkip {
		t.Fatalf("decision = %q, want %q", got.Decision, MergeSkipSkip)
	}
	if got.Reason != SkipProblemNothingToMerge {
		t.Fatalf("reason = %q, want %q", got.Reason, SkipProblemNothingToMerge)
	}
}

func TestClassifyMergeSkipEmptyAndDirtyBranchHolds(t *testing.T) {
	got := ClassifyMergeSkip([]string{SkipProblemNothingToMerge, SkipProblemMainDirty})
	if got.Decision != MergeSkipHold {
		t.Fatalf("decision = %q, want %q", got.Decision, MergeSkipHold)
	}
	if got.Reason != "main_dirty+nothing_to_merge" {
		t.Fatalf("reason = %q, want sorted problem codes", got.Reason)
	}
}

func TestClassifyMergeSkipReadyElement(t *testing.T) {
	got := ClassifyMergeSkip(nil)
	if got.Decision != MergeSkipMerge {
		t.Fatalf("decision = %q, want %q", got.Decision, MergeSkipMerge)
	}
	if got.Reason != "ready" {
		t.Fatalf("reason = %q, want ready", got.Reason)
	}
}

func TestClassifyMergeSkipOtherProblemsHold(t *testing.T) {
	tests := []struct {
		name       string
		problems   []string
		wantReason string
	}{
		{"merge conflict", []string{SkipProblemMergeConflict}, "merge_conflict"},
		{"uncommitted worktree", []string{SkipProblemWorktreeUncommitted}, "worktree_uncommitted"},
		{"unknown code", []string{"not_a_real_problem"}, "not_a_real_problem"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyMergeSkip(tt.problems)
			if got.Decision != MergeSkipHold {
				t.Fatalf("decision = %q, want %q", got.Decision, MergeSkipHold)
			}
			if got.Reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.wantReason)
			}
		})
	}
}

func TestClassifyMergeSkipNormalizesCodes(t *testing.T) {
	got := ClassifyMergeSkip([]string{" nothing_to_merge ", "", "nothing_to_merge"})
	if got.Decision != MergeSkipSkip {
		t.Fatalf("decision = %q, want %q", got.Decision, MergeSkipSkip)
	}
	if got.Reason != SkipProblemNothingToMerge {
		t.Fatalf("reason = %q, want %q", got.Reason, SkipProblemNothingToMerge)
	}
}

func TestClassifyMergeSkipSortsReasons(t *testing.T) {
	got := ClassifyMergeSkip([]string{
		SkipProblemWorktreeUncommitted,
		SkipProblemMainDirty,
		SkipProblemNothingToMerge,
	})
	if got.Decision != MergeSkipHold {
		t.Fatalf("decision = %q, want %q", got.Decision, MergeSkipHold)
	}
	want := "main_dirty+nothing_to_merge+worktree_uncommitted"
	if got.Reason != want {
		t.Fatalf("reason = %q, want %q", got.Reason, want)
	}
}

func mergeSkipIDs(items []MergeSkipItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func TestBuildMergeSkipReportKeepsSkipsOutOfMergeCalls(t *testing.T) {
	report := BuildMergeSkipReport([]MergeSkipItem{
		{ID: "w1"},
		{ID: "w2", Problems: []string{SkipProblemNothingToMerge}},
		{ID: "w3", Problems: []string{SkipProblemNothingToMerge, SkipProblemMainDirty}},
		{ID: "w4", Problems: []string{SkipProblemMergeConflict}},
	})

	if got := strings.Join(mergeSkipIDs(report.Merge), ","); got != "w1" {
		t.Fatalf("merge call list = %q, want w1", got)
	}
	if got := strings.Join(mergeSkipIDs(report.Skipped), ","); got != "w2" {
		t.Fatalf("skip list = %q, want w2", got)
	}
	if got := strings.Join(mergeSkipIDs(report.Held), ","); got != "w3,w4" {
		t.Fatalf("hold list = %q, want w3,w4", got)
	}
	if len(report.Skipped) != 1 || len(report.Held) != 2 {
		t.Fatalf("counts: skipped=%d held=%d, want 1 and 2", len(report.Skipped), len(report.Held))
	}
}

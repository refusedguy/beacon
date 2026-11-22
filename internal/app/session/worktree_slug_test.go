package session

import (
	"strings"
	"testing"
)

func TestSlugHashIsStableAndBounded(t *testing.T) {
	first := SlugHash("feature/fix", 1)
	if first != SlugHash("feature/fix", 1) {
		t.Fatal("SlugHash changed between calls")
	}
	if len(first) != SlugHashLength {
		t.Fatalf("suffix length = %d, want %d", len(first), SlugHashLength)
	}
	for _, r := range first {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			t.Fatalf("suffix %q is not hex", first)
		}
	}
	if first == SlugHash("feature/fix", 2) {
		t.Fatal("different attempts produced the same suffix")
	}
}

func TestBranchSlugSanitizesBranchName(t *testing.T) {
	cases := []struct {
		branch string
		want   string
	}{
		{"feature/fix", "fix"},
		{"refs/heads/Feature_Fix.V2", "feature-fix-v2"},
		{"///", defaultWorktreeSlug},
	}
	for _, tc := range cases {
		if got := BranchSlug(tc.branch, nil); got != tc.want {
			t.Errorf("BranchSlug(%q) = %q, want %q", tc.branch, got, tc.want)
		}
	}
}

func TestBranchSlugSeparatesBranchesWithSameLastPart(t *testing.T) {
	first := BranchSlug("repo-a/feature/fix", nil)
	if first != "fix" {
		t.Fatalf("first slug = %q, want fix", first)
	}

	second := BranchSlug("repo-b/feature/fix", []string{first})
	suffix := suffixAfter(t, second, "fix")
	if second == first {
		t.Fatal("two branches got the same slug")
	}
	if want := SlugHash("repo-b/feature/fix", 1); suffix != want {
		t.Fatalf("suffix = %q, want %q", suffix, want)
	}

	reverse := BranchSlug("repo-b/feature/fix", []string{first, "unrelated"})
	if reverse != second {
		t.Fatalf("slug changed with extra occupied names: %q vs %q", reverse, second)
	}
}

func TestBranchSlugSkipsOccupiedSuffix(t *testing.T) {
	branch := "repo/feature/fix"
	first := "fix-" + SlugHash(branch, 1)
	got := BranchSlug(branch, []string{"fix", first})
	want := "fix-" + SlugHash(branch, 2)
	if got != want {
		t.Fatalf("slug = %q, want %q", got, want)
	}
}

func TestPlanWorktreeReusesCheckedOutBranch(t *testing.T) {
	branch := "feature/fix"
	existing := []Worktree{
		{Name: "cd10", Branch: "refs/heads/" + branch},
	}
	plan := PlanWorktree(branch, []string{"cd10"}, existing)
	if plan.Action != WorktreeReuse {
		t.Fatalf("action = %q, want %q", plan.Action, WorktreeReuse)
	}
	if plan.Name != "cd10" {
		t.Fatalf("reused name = %q, want cd10", plan.Name)
	}
}

func TestPlanWorktreeCreatesSlugWhenBranchIsFree(t *testing.T) {
	existing := []Worktree{
		{Name: "cd10", Branch: "feature/other"},
	}
	plan := PlanWorktree("repo/feature/fix", []string{}, existing)
	if plan.Action != WorktreeCreate {
		t.Fatalf("action = %q, want %q", plan.Action, WorktreeCreate)
	}
	if plan.Name != "fix" {
		t.Fatalf("name = %q, want fix", plan.Name)
	}

	plan = PlanWorktree("repo/feature/fix", []string{"fix"}, existing)
	suffix := suffixAfter(t, plan.Name, "fix")
	if want := SlugHash("repo/feature/fix", 1); suffix != want {
		t.Fatalf("suffix = %q, want %q", suffix, want)
	}
}

func TestPlanWorktreeIgnoresInputOrder(t *testing.T) {
	branch := "repo/feature/fix"
	taken := []string{"fix", "other"}
	existing := []Worktree{
		{Name: "aa", Branch: "one/branch"},
		{Name: "zz", Branch: "two/branch"},
	}

	want := PlanWorktree(branch, taken, existing)
	got := PlanWorktree(branch, []string{"other", "fix"}, []Worktree{existing[1], existing[0]})
	if got != want {
		t.Fatalf("plan changed with input order: %+v vs %+v", got, want)
	}
}

func TestPlanWorktreePicksSmallestNameOnReuseTie(t *testing.T) {
	branch := "feature/fix"
	existing := []Worktree{
		{Name: "zeta", Branch: branch},
		{Name: "alpha", Branch: branch},
	}
	want := WorktreePlan{Name: "alpha", Action: WorktreeReuse}
	for _, order := range [][]Worktree{existing, []Worktree{existing[1], existing[0]}} {
		if got := PlanWorktree(branch, nil, order); got != want {
			t.Errorf("plan = %+v, want %+v", got, want)
		}
	}
}

func suffixAfter(t *testing.T, slug, base string) string {
	t.Helper()
	prefix := base + "-"
	if !strings.HasPrefix(slug, prefix) {
		t.Fatalf("slug %q does not extend base %q", slug, base)
	}
	suffix := strings.TrimPrefix(slug, prefix)
	if len(suffix) != SlugHashLength {
		t.Fatalf("suffix %q length = %d, want %d", suffix, len(suffix), SlugHashLength)
	}
	return suffix
}

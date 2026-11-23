package tasksource

import (
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

const epicIssueBody = `Epic issue body.

` + "```yaml\n" + `id: T-EPIC
kind: epic
base_branch: main
require_review: human
scope:
  - internal/app/
priority: 3
plan:
  - title: First child
    scope: [internal/app/a.go, internal/**/*.go]
    depends: [T-BASE]
    priority: 9
  - title: Second child
    scope: internal/infra/b.go
    depends: T-BASE
    priority: 4
` + "```\n"

func TestParseTaskFieldsEpic(t *testing.T) {
	got := ParseTaskFields(epicIssueBody, "", "")

	if got.ID != "T-EPIC" || got.Kind != core.TaskKindEpic {
		t.Fatalf("identity = %q, %q", got.ID, got.Kind)
	}
	if got.BaseBranch != "main" || got.RequireReview != core.ReviewModeHuman {
		t.Fatalf("branch/review = %q, %q", got.BaseBranch, got.RequireReview)
	}
	if len(got.Plan) != 2 {
		t.Fatalf("plan = %+v", got.Plan)
	}
	first := got.Plan[0]
	if first.Title != "First child" || first.Priority != 9 {
		t.Fatalf("first child = %+v", first)
	}
	wantScope := []core.ScopePath{"f:internal/app/a.go", "g:internal/**/*.go"}
	if len(first.Scope) != len(wantScope) {
		t.Fatalf("first scope = %v", first.Scope)
	}
	for i := range wantScope {
		if first.Scope[i] != wantScope[i] {
			t.Fatalf("first scope = %v, want %v", first.Scope, wantScope)
		}
	}
	if len(first.Depends) != 1 || first.Depends[0] != "T-BASE" {
		t.Fatalf("first depends = %v", first.Depends)
	}
	second := got.Plan[1]
	if second.Title != "Second child" || second.Priority != 4 || len(second.Scope) != 1 ||
		second.Scope[0] != "f:internal/infra/b.go" || len(second.Depends) != 1 || second.Depends[0] != "T-BASE" {
		t.Fatalf("second child = %+v", second)
	}
}

func TestParseTaskFieldsFlatEpicLines(t *testing.T) {
	body := "TaskID: T-FLAT\nKind: EPIC\nBaseBranch: release/1\nRequireReview: none\n"
	got := ParseTaskFields(body, "", "")

	if got.ID != "T-FLAT" || got.Kind != core.TaskKindEpic ||
		got.BaseBranch != "release/1" || got.RequireReview != core.ReviewModeNone {
		t.Fatalf("flat fields = %+v", got)
	}
}

func TestPlanParserChildren(t *testing.T) {
	epic := core.Task{
		ID:   "T-EPIC",
		Kind: core.TaskKindEpic,
		Plan: []core.ChildSpec{
			{
				Title:    "First child",
				Scope:    []core.ScopePath{core.ScopeFile("internal/app/a.go")},
				Depends:  []core.TaskID{"T-BASE"},
				Priority: 9,
			},
			{Title: "Second child", Priority: 4},
		},
	}

	children := (PlanParser{}).Children(epic)
	if len(children) != 2 {
		t.Fatalf("children = %+v", children)
	}
	first := children[0]
	if first.ID != "T-EPIC-01" || first.Title != "First child" || first.Priority != 9 ||
		first.Kind != core.TaskKindTask || first.Status != core.TaskAvailable {
		t.Fatalf("first child = %+v", first)
	}
	if len(first.Scope) != 1 || first.Scope[0] != "f:internal/app/a.go" {
		t.Fatalf("first scope = %v", first.Scope)
	}
	if len(first.DependsOn) != 1 || first.DependsOn[0] != "T-BASE" {
		t.Fatalf("first depends = %v", first.DependsOn)
	}
	second := children[1]
	if second.ID != "T-EPIC-02" || second.Title != "Second child" || second.Priority != 4 {
		t.Fatalf("second child = %+v", second)
	}
	if len(second.Scope) != 0 || len(second.DependsOn) != 0 {
		t.Fatalf("second child carried unexpected scope or depends: %+v", second)
	}
}

func TestPlanParserNumbersAfterNine(t *testing.T) {
	epic := core.Task{ID: "T-EPIC", Plan: make([]core.ChildSpec, 10)}
	children := (PlanParser{}).Children(epic)

	if children[8].ID != "T-EPIC-09" || children[9].ID != "T-EPIC-10" {
		t.Fatalf("last ids = %q, %q", children[8].ID, children[9].ID)
	}
}

func TestNormalizeEpicFields(t *testing.T) {
	kinds := []struct {
		in   string
		want core.TaskKind
	}{
		{"epic", core.TaskKindEpic},
		{" EPIC ", core.TaskKindEpic},
		{"task", core.TaskKindTask},
		{"", core.TaskKindTask},
	}
	for _, tc := range kinds {
		if got := normalizeKind(tc.in); got != tc.want {
			t.Errorf("normalizeKind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	reviews := []struct {
		in   string
		want core.ReviewMode
	}{
		{"", core.ReviewModeLLM},
		{"llm", core.ReviewModeLLM},
		{"human", core.ReviewModeHuman},
		{"manual", core.ReviewModeHuman},
		{"none", core.ReviewModeNone},
		{"skip", core.ReviewModeNone},
	}
	for _, tc := range reviews {
		if got := normalizeReview(tc.in); got != tc.want {
			t.Errorf("normalizeReview(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

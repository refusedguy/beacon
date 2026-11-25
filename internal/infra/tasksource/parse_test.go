package tasksource

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func captureTaskSourceLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

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
    body: Implement the first child.
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
	if first.Body != "Implement the first child." {
		t.Fatalf("first child body = %q", first.Body)
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
	if second.Body != "" {
		t.Fatalf("second child body = %q, want empty", second.Body)
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

func TestParseTaskFieldsParent(t *testing.T) {
	body := "TaskID: T-CHILD\nParent: T-EPIC\n"
	got := ParseTaskFields(body, "", "")

	if got.ParentID() != "T-EPIC" {
		t.Fatalf("parent = %q, want T-EPIC", got.ParentID())
	}
	yamlBody := "```yaml\nid: T-CHILD\nparent: T-EPIC\n```\n"
	if other := ParseTaskFields(yamlBody, "", ""); other.ParentID() != "T-EPIC" {
		t.Fatalf("yaml parent = %q", other.ParentID())
	}
}

func TestParseTaskFieldsReusesIdenticalText(t *testing.T) {
	body := "TaskID: T-CACHE\nScope: a.go\nDepends: 1\n"
	first := ParseTaskFields(body, "docs/tasks", "GH")
	second := ParseTaskFields(body, "docs/tasks", "GH")
	fresh := parseTaskFields(body, "docs/tasks", "GH")

	if !reflect.DeepEqual(first, fresh) || !reflect.DeepEqual(second, fresh) {
		t.Fatalf("cached parse differs from fresh parse: first=%+v second=%+v fresh=%+v", first, second, fresh)
	}

	other := ParseTaskFields(body, "docs/tasks", "GL")
	if len(other.Depends) != 1 || other.Depends[0] != "GL-1" {
		t.Fatalf("dependency prefix from a different key = %v", other.Depends)
	}
}

func TestParseTaskFieldsCacheReturnsIndependentSlices(t *testing.T) {
	body := "TaskID: T-ISO\nScope: a.go\nDepends: 1\n"
	first := ParseTaskFields(body, "", "")
	first.Scope[0] = "changed.go"
	first.Depends[0] = "changed"

	second := ParseTaskFields(body, "", "")
	if second.Scope[0] != "a.go" || second.Depends[0] != "1" {
		t.Fatalf("cache leaked mutated fields: %+v", second)
	}
}

func TestParseTaskFieldsCacheSeparatesBodies(t *testing.T) {
	one := "TaskID: T-CACHE-ONE\nScope: a.go\n"
	two := "TaskID: T-CACHE-TWO\nScope: b.go\n"
	first := ParseTaskFields(one, "", "")
	ParseTaskFields(two, "", "")
	again := ParseTaskFields(one, "", "")

	if first.ID != "T-CACHE-ONE" || again.ID != "T-CACHE-ONE" {
		t.Fatalf("cache mixed two bodies: %q and %q", first.ID, again.ID)
	}
	if len(first.Scope) != 1 || first.Scope[0] != "a.go" {
		t.Fatalf("scope = %v", first.Scope)
	}
}

func TestParseTaskFieldsMalformedYAMLReported(t *testing.T) {
	logs := captureTaskSourceLogs(t)
	body := "Issue body.\n\n" + "```yaml\n" + "id: [unclosed\npriority: 5\n" + "```\n"

	got := ParseTaskFields(body, "", "")
	if got.ID != "" {
		t.Fatalf("ID = %q, want empty when YAML fails to parse", got.ID)
	}
	if !strings.Contains(logs.String(), "malformed YAML field block") {
		t.Fatalf("malformed YAML not reported: %s", logs.String())
	}
}

func TestPlanParserChildren(t *testing.T) {
	epic := core.Task{
		ID:   "T-EPIC",
		Kind: core.TaskKindEpic,
		Plan: []core.ChildSpec{
			{
				Title:    "First child",
				Body:     "Implement the first child.",
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
		first.Kind != core.TaskKindTask || first.Status != core.TaskAvailable || first.Parent != "T-EPIC" {
		t.Fatalf("first child = %+v", first)
	}
	if first.Body != "Implement the first child." {
		t.Fatalf("first child body = %q", first.Body)
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
	if second.Body != "" {
		t.Fatalf("second child body = %q, want empty", second.Body)
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

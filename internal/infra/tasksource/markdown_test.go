package tasksource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

const legacyCard = `# T-1. Fix parser
Статус: todo
Владелец файлов: internal/app/a.go, internal/app/b.go
Зависимости: T-0
Фаза: P1
Ссылка: https://example.com/1
`

const frontmatterCard = `---
id: T-2
title: Add webhook
status: available
scope:
  - internal/**/*.go
  - internal/infra/config/
  - internal/infra/scm/webhook.go
depends: [T-1]
priority: 7
source_ref: https://example.com/2
phase: P2
---
# T-2. Wrong title
Body line.
`

func TestMarkdownCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "docs", "tasks")
	s := NewMarkdown(dir)
	task := core.Task{
		ID:        "T-20261123-abcdef12",
		Title:     "Add task creation tool",
		Body:      "Create a card in one call.",
		Scope:     []core.ScopePath{core.ScopeFile("internal/app/a.go"), core.ScopePattern("internal/**/*.go"), core.ScopeDir("internal/infra/")},
		DependsOn: []core.TaskID{"E2E-03"},
		Priority:  11,
	}

	created, err := s.Create(context.Background(), task)
	if err != nil {
		t.Fatalf("s.Create(context.Background(), task): %v", err)
	}
	wantPath := filepath.Join(dir, string(task.ID)+".md")
	if created.SourceRef != wantPath || created.Status != core.TaskAvailable {
		t.Fatalf("created = %+v", created)
	}
	b, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("os.ReadFile(wantPath): %v", err)
	}
	for _, want := range []string{"id: T-20261123-abcdef12", "status: available", "priority: 11", "E2E-03", "internal/app/a.go"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("card missing %q:\n%s", want, b)
		}
	}
	if !strings.Contains(string(b), "internal/infra/") {
		t.Errorf("card lost the directory scope:\n%s", b)
	}
	tasks, err := s.ListAvailable(context.Background())
	if err != nil {
		t.Fatalf("s.ListAvailable(context.Background()): %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	got := tasks[0]
	if got.ID != task.ID || got.Title != task.Title || got.Priority != 11 || len(got.DependsOn) != 1 {
		t.Fatalf("parsed task = %+v", got)
	}
	if got.Body != task.Body {
		t.Fatalf("body = %q", got.Body)
	}
	if len(got.Scope) != 3 || !got.Scope[1].CoversPath("internal/infra/mcp/server.go") ||
		!got.Scope[2].CoversPath("internal/infra/mcp/server.go") {
		t.Fatalf("scope = %v", got.Scope)
	}
	if _, err := s.Create(context.Background(), task); err == nil {
		t.Fatal("second Create overwrote the card")
	}
}

func TestMarkdownLegacyLifecycle(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "archive")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(sub, 0o755): %v", err)
	}
	path := filepath.Join(sub, "legacy-card.md")
	if err := os.WriteFile(path, []byte(legacyCard), 0o644); err != nil {
		t.Fatalf("os.WriteFile(path, []byte(legacyCard), 0o644): %v", err)
	}
	s := NewMarkdown(dir)
	tasks, err := s.ListAvailable(context.Background())
	if err != nil {
		t.Fatalf("s.ListAvailable(context.Background()): %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("want 1 task, got %d: %+v", len(tasks), tasks)
	}
	got := tasks[0]
	if got.ID != "T-1" || got.Title != "Fix parser" || got.Status != core.TaskAvailable {
		t.Fatalf("bad task identity: %+v", got)
	}
	if len(got.Scope) != 2 || got.Scope[0] != "f:internal/app/a.go" || got.Scope[1] != "f:internal/app/b.go" {
		t.Fatalf("bad scope: %+v", got.Scope)
	}
	if len(got.DependsOn) != 1 || got.DependsOn[0] != "T-0" {
		t.Fatalf("bad depends: %+v", got.DependsOn)
	}
	if got.SourceRef != path {
		t.Fatalf("SourceRef = %q, want %q", got.SourceRef, path)
	}
	if err := s.Claim(context.Background(), "T-1"); err != nil {
		t.Fatalf("s.Claim(context.Background(), \"T-1\"): %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(path): %v", err)
	}
	if !strings.Contains(string(b), "Статус: in_progress") {
		t.Fatalf("claim did not write in_progress: %s", b)
	}
	tasks, err = s.ListAvailable(context.Background())
	if err != nil {
		t.Fatalf("s.ListAvailable(context.Background()): %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("claimed task still available: %+v", tasks)
	}
	if err := s.Complete(context.Background(), "T-1"); err != nil {
		t.Fatalf("s.Complete(context.Background(), \"T-1\"): %v", err)
	}
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), "Статус: done") {
		t.Fatalf("complete did not write done: %s", b)
	}
}

func TestMarkdownFrontmatter(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "infra", "config"), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(filepath.Join(dir, \"internal\", \"infra\", \"config\"), 0o755): %v", err)
	}
	path := filepath.Join(dir, "frontmatter.md")
	if err := os.WriteFile(path, []byte(frontmatterCard), 0o644); err != nil {
		t.Fatalf("os.WriteFile(path, []byte(frontmatterCard), 0o644): %v", err)
	}
	s := NewMarkdown(dir)
	tasks, err := s.ListAvailable(context.Background())
	if err != nil {
		t.Fatalf("s.ListAvailable(context.Background()): %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("want 1 task, got %d: %+v", len(tasks), tasks)
	}
	got := tasks[0]
	if got.ID != "T-2" || got.Title != "Add webhook" || got.Priority != 7 {
		t.Fatalf("bad frontmatter identity: %+v", got)
	}
	if got.Status != core.TaskAvailable {
		t.Fatalf("status = %q, want available", got.Status)
	}
	if len(got.DependsOn) != 1 || got.DependsOn[0] != "T-1" {
		t.Fatalf("bad depends: %+v", got.DependsOn)
	}
	wantScope := []core.ScopePath{"g:internal/**/*.go", "d:internal/infra/config", "f:internal/infra/scm/webhook.go"}
	if len(got.Scope) != len(wantScope) {
		t.Fatalf("scope = %+v, want %+v", got.Scope, wantScope)
	}
	for i := range wantScope {
		if got.Scope[i] != wantScope[i] {
			t.Fatalf("scope = %+v, want %+v", got.Scope, wantScope)
		}
	}
	if got.SourceRef != path {
		t.Fatalf("SourceRef = %q, want %q", got.SourceRef, path)
	}
	if err := s.Claim(context.Background(), "T-2"); err != nil {
		t.Fatalf("s.Claim(context.Background(), \"T-2\"): %v", err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "status: in_progress") {
		t.Fatalf("claim did not write in_progress: %s", b)
	}
	tasks, _ = s.ListAvailable(context.Background())
	if len(tasks) != 0 {
		t.Fatalf("claimed frontmatter task still available: %+v", tasks)
	}
}

func TestMarkdownFrontmatterAliases(t *testing.T) {
	dir := t.TempDir()
	card := "---\ntask_id: T-ALIAS\ntitle: Alias card\nstatus: available\nowner_files: [internal/app/a.go]\n---\n# T-ALIAS. Wrong title\nBody.\n"
	path := filepath.Join(dir, "odd-name.md")
	if err := os.WriteFile(path, []byte(card), 0o644); err != nil {
		t.Fatalf("os.WriteFile(path, []byte(card), 0o644): %v", err)
	}
	s := NewMarkdown(dir)

	tasks, err := s.ListAvailable(context.Background())
	if err != nil {
		t.Fatalf("s.ListAvailable(context.Background()): %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	got := tasks[0]
	if got.ID != "T-ALIAS" || got.Title != "Alias card" {
		t.Fatalf("identity = %+v", got)
	}
	if len(got.Scope) != 1 || got.Scope[0] != "f:internal/app/a.go" {
		t.Fatalf("scope = %v", got.Scope)
	}
	if err := s.Claim(context.Background(), "T-ALIAS"); err != nil {
		t.Fatalf("s.Claim(context.Background(), \"T-ALIAS\"): %v", err)
	}
}

func TestMarkdownMarkInWorkspace(t *testing.T) {
	root := t.TempDir()
	cardDir := filepath.Join(root, "docs", "tasks")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(cardDir, 0o755): %v", err)
	}
	card := "---\nid: T-9\ntitle: Workspace card\nstatus: available\n---\n# T-9. Workspace card\n"
	path := filepath.Join(cardDir, "T-9.md")
	if err := os.WriteFile(path, []byte(card), 0o644); err != nil {
		t.Fatalf("os.WriteFile(path, []byte(card), 0o644): %v", err)
	}
	s := NewMarkdown(filepath.Join(root, "orchestrator-cards"))

	if err := s.MarkInWorkspace(context.Background(), "T-9", root, "in_progress"); err != nil {
		t.Fatalf("MarkInWorkspace(in_progress) error = %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(path): %v", err)
	}
	if !strings.Contains(string(b), "status: in_progress") {
		t.Fatalf("card not marked in progress: %s", b)
	}
	if err := s.MarkInWorkspace(context.Background(), "T-9", root, "done"); err != nil {
		t.Fatalf("MarkInWorkspace(done) error = %v", err)
	}
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), "status: done") {
		t.Fatalf("card not marked done: %s", b)
	}
	if err := s.MarkInWorkspace(context.Background(), "T-MISSING", root, "done"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("MarkInWorkspace(missing) error = %v, want os.ErrNotExist", err)
	}
}

func TestCardStorePatchStatus(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "odd-name.md")
	if err := os.WriteFile(legacy, []byte("# T-PATCH. Legacy\nBody line.\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(legacy, []byte(\"# T-PATCH. Legacy\\nBody line.\\n\"), 0o644): %v", err)
	}
	fm := filepath.Join(dir, "also-odd.md")
	if err := os.WriteFile(fm, []byte("---\nid: T-FM\ntitle: Front\n---\n# T-FM. Wrong\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(fm, []byte(\"---\\nid: T-FM\\ntitle: Front\\n---\\n# T-FM. Wrong\\n\"), 0o644): %v", err)
	}
	store := CardStore{Dir: dir}

	// Lookup follows the parsed ID, not the filename, and a missing status
	// line is inserted in the card's own format.
	if err := store.PatchStatus(context.Background(), "T-PATCH", "in_progress"); err != nil {
		t.Fatalf("store.PatchStatus(context.Background(), \"T-PATCH\", \"in_progress\"): %v", err)
	}
	b, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatalf("os.ReadFile(legacy): %v", err)
	}
	if !strings.Contains(string(b), "Статус: in_progress") {
		t.Fatalf("legacy status not written: %s", b)
	}
	if err := store.PatchStatus(context.Background(), "T-FM", "done"); err != nil {
		t.Fatalf("store.PatchStatus(context.Background(), \"T-FM\", \"done\"): %v", err)
	}
	b, _ = os.ReadFile(fm)
	if !strings.Contains(string(b), "status: done") {
		t.Fatalf("frontmatter status not written: %s", b)
	}

	// A second patch updates the existing line in place rather than adding one.
	if err := store.PatchStatus(context.Background(), "T-FM", "in_progress"); err != nil {
		t.Fatalf("store.PatchStatus(context.Background(), \"T-FM\", \"in_progress\"): %v", err)
	}
	b, _ = os.ReadFile(fm)
	if strings.Count(string(b), "status:") != 1 || !strings.Contains(string(b), "status: in_progress") {
		t.Fatalf("frontmatter status not replaced: %s", b)
	}

	if err := store.PatchStatus(context.Background(), "T-MISSING", "done"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing card error = %v, want os.ErrNotExist", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.PatchStatus(ctx, "T-FM", "done"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled patch error = %v, want context.Canceled", err)
	}
}

func TestNormalizeStatus(t *testing.T) {
	cases := []struct {
		raw  string
		want core.TaskStatus
	}{
		{"todo", core.TaskAvailable},
		{"available", core.TaskAvailable},
		{"in_progress", core.TaskClaimed},
		{"в работе", core.TaskClaimed},
		{"done", core.TaskDone},
		{"dropped", core.TaskDropped},
		{"closed", core.TaskDone},
	}
	for _, tc := range cases {
		got := normalizeStatus(tc.raw)
		if got != tc.want {
			t.Errorf("normalizeStatus(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

const epicCard = `---
id: T-EPIC
title: Big epic
status: available
kind: epic
base_branch: main
require_review: human
scope:
  - internal/app/
depends: [T-BASE]
priority: 5
plan:
  - title: Foo child
    body: Split off the foo work.
    scope: [internal/app/foo.go]
    depends: [T-BASE]
    priority: 8
  - title: Bar child
    scope: internal/app/bar.go
    priority: 3
---
# T-EPIC. Big epic
Split this work.
`

func TestMarkdownFrontmatterEpic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "T-EPIC.md")
	if err := os.WriteFile(path, []byte(epicCard), 0o644); err != nil {
		t.Fatalf("os.WriteFile(path, []byte(epicCard), 0o644): %v", err)
	}
	s := NewMarkdown(dir)

	tasks, err := s.ListAvailable(context.Background())
	if err != nil {
		t.Fatalf("s.ListAvailable(context.Background()): %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	got := tasks[0]
	if got.ID != "T-EPIC" || got.Kind != core.TaskKindEpic || got.BaseBranch != "main" ||
		got.RequireReview != core.ReviewModeHuman {
		t.Fatalf("epic fields = %+v", got)
	}
	if len(got.Plan) != 2 {
		t.Fatalf("plan = %+v", got.Plan)
	}
	if got.Plan[0].Title != "Foo child" || got.Plan[0].Priority != 8 ||
		len(got.Plan[0].Scope) != 1 || got.Plan[0].Scope[0] != "f:internal/app/foo.go" ||
		len(got.Plan[0].Depends) != 1 || got.Plan[0].Depends[0] != "T-BASE" {
		t.Fatalf("first plan entry = %+v", got.Plan[0])
	}
	if got.Plan[0].Body != "Split off the foo work." {
		t.Fatalf("first plan body = %q", got.Plan[0].Body)
	}
	if got.Plan[1].Title != "Bar child" || got.Plan[1].Priority != 3 {
		t.Fatalf("second plan entry = %+v", got.Plan[1])
	}
	if got.Plan[1].Body != "" {
		t.Fatalf("second plan body = %q, want empty", got.Plan[1].Body)
	}

	children, err := s.Children(context.Background(), "T-EPIC")
	if err != nil {
		t.Fatalf("s.Children(context.Background(), \"T-EPIC\"): %v", err)
	}
	if len(children) != 2 || children[0].ID != "T-EPIC-01" || children[1].ID != "T-EPIC-02" {
		t.Fatalf("children = %+v", children)
	}
	if children[0].Title != "Foo child" || children[0].Priority != 8 || children[0].Kind != core.TaskKindTask {
		t.Fatalf("first child = %+v", children[0])
	}
	if children[0].Body != "Split off the foo work." {
		t.Fatalf("first child body = %q", children[0].Body)
	}
}

func TestMarkdownCreateEpic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "docs", "tasks")
	s := NewMarkdown(dir)
	task := core.Task{
		ID:            "T-EPIC-2",
		Title:         "Create epic",
		Body:          "Epic body.",
		Kind:          core.TaskKindEpic,
		BaseBranch:    "main",
		RequireReview: core.ReviewModeNone,
		Plan: []core.ChildSpec{{
			Title:    "Child one",
			Body:     "Handle the create path.",
			Scope:    []core.ScopePath{core.ScopeFile("internal/app/create.go")},
			Depends:  []core.TaskID{"T-BASE"},
			Priority: 6,
		}},
	}

	created, err := s.Create(context.Background(), task)
	if err != nil {
		t.Fatalf("s.Create(context.Background(), task): %v", err)
	}
	wantPath := filepath.Join(dir, "T-EPIC-2.md")
	if created.SourceRef != wantPath {
		t.Fatalf("SourceRef = %q", created.SourceRef)
	}
	b, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("os.ReadFile(wantPath): %v", err)
	}
	for _, want := range []string{"kind: epic", "base_branch: main", "require_review: none", "title: Child one", "body: Handle the create path.", "priority: 6", "internal/app/create.go"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("card missing %q:\n%s", want, b)
		}
	}

	tasks, err := s.ListAvailable(context.Background())
	if err != nil {
		t.Fatalf("s.ListAvailable(context.Background()): %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	got := tasks[0]
	if got.Kind != core.TaskKindEpic || got.BaseBranch != "main" || got.RequireReview != core.ReviewModeNone {
		t.Fatalf("round-trip epic fields = %+v", got)
	}
	if len(got.Plan) != 1 || got.Plan[0].Title != "Child one" || got.Plan[0].Priority != 6 ||
		len(got.Plan[0].Scope) != 1 || got.Plan[0].Scope[0] != "f:internal/app/create.go" ||
		len(got.Plan[0].Depends) != 1 || got.Plan[0].Depends[0] != "T-BASE" {
		t.Fatalf("round-trip plan = %+v", got.Plan)
	}
	if got.Plan[0].Body != "Handle the create path." {
		t.Fatalf("round-trip child body = %q", got.Plan[0].Body)
	}
	children, err := s.Children(context.Background(), "T-EPIC-2")
	if err != nil {
		t.Fatalf("s.Children(context.Background(), \"T-EPIC-2\"): %v", err)
	}
	if len(children) != 1 || children[0].ID != "T-EPIC-2-01" || children[0].Title != "Child one" {
		t.Fatalf("children = %+v", children)
	}
	if children[0].Body != "Handle the create path." {
		t.Fatalf("child body = %q", children[0].Body)
	}
}

type stubEpicParser struct {
	calls int
}

func (p *stubEpicParser) Children(core.Task) []core.Task {
	p.calls++
	return []core.Task{{ID: "STUB-01"}}
}

func TestMarkdownChildrenUsesInjectedParser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "T-EPIC.md")
	if err := os.WriteFile(path, []byte(epicCard), 0o644); err != nil {
		t.Fatalf("os.WriteFile(path, []byte(epicCard), 0o644): %v", err)
	}
	stub := &stubEpicParser{}
	s := NewMarkdown(dir)
	s.Parser = stub

	children, err := s.Children(context.Background(), "T-EPIC")
	if err != nil {
		t.Fatalf("s.Children(context.Background(), \"T-EPIC\"): %v", err)
	}
	if stub.calls != 1 || len(children) != 1 || children[0].ID != "STUB-01" {
		t.Fatalf("calls = %d, children = %+v", stub.calls, children)
	}
}

func TestMarkdownChildrenOfTaskIsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "T-PLAIN.md")
	card := "---\nid: T-PLAIN\ntitle: Plain task\nstatus: available\n---\n"
	if err := os.WriteFile(path, []byte(card), 0o644); err != nil {
		t.Fatalf("os.WriteFile(path, []byte(card), 0o644): %v", err)
	}
	children, err := NewMarkdown(dir).Children(context.Background(), "T-PLAIN")
	if err != nil {
		t.Fatalf("NewMarkdown(dir).Children(context.Background(), \"T-PLAIN\"): %v", err)
	}
	if len(children) != 0 {
		t.Fatalf("children = %+v", children)
	}
}

func TestMarkdownHonorsCancelledContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "T-CANCEL.md")
	if err := os.WriteFile(path, []byte(frontmatterCard), 0o644); err != nil {
		t.Fatalf("os.WriteFile(path, []byte(frontmatterCard), 0o644): %v", err)
	}
	s := NewMarkdown(dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.ListAvailable(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListAvailable error = %v, want context.Canceled", err)
	}
	if _, err := s.Children(ctx, "T-2"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Children error = %v, want context.Canceled", err)
	}
	task := core.Task{ID: "T-NEW", Title: "New card"}
	if _, err := s.Create(ctx, task); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "T-NEW.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Create wrote a card after cancellation: %v", err)
	}
}

func TestMarkdownMalformedFrontmatterReported(t *testing.T) {
	logs := captureTaskSourceLogs(t)
	content := "---\nid: [unclosed\ntitle: Broken\n---\n# T-BROKEN. Broken\n"

	if _, ok := parseCard(content, filepath.Join(t.TempDir(), "T-BROKEN.md")); ok {
		t.Fatal("malformed frontmatter parsed as a card")
	}
	if !strings.Contains(logs.String(), "malformed card frontmatter") {
		t.Fatalf("malformed frontmatter not reported: %s", logs.String())
	}
}

func TestScopeClassification(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(filepath.Join(dir, \"pkg\"), 0o755): %v", err)
	}
	card := "# T-3. Scope\nВладелец файлов: internal/**/*.go, pkg/, pkg/core/task.go\n"
	got, ok := parseCard(card, filepath.Join(dir, "T-3.md"))
	if !ok {
		t.Fatal("card not parsed")
	}
	want := []core.ScopePath{"g:internal/**/*.go", "d:pkg", "f:pkg/core/task.go"}
	if len(got.Scope) != len(want) {
		t.Fatalf("scope = %+v, want %+v", got.Scope, want)
	}
	for i := range want {
		if got.Scope[i] != want[i] {
			t.Fatalf("scope = %+v, want %+v", got.Scope, want)
		}
	}
}

// resetCardIndex drops the cached index for dir so a test can count the scans
// its own operations cause.
func resetCardIndex(dir string) {
	cardIndexCache.mu.Lock()
	defer cardIndexCache.mu.Unlock()
	delete(cardIndexCache.byDir, cardIndexKey(dir))
}

func writeIndexCard(t testing.TB, dir, id, status string) string {
	t.Helper()
	content := "---\nid: " + id + "\ntitle: Card " + id + "\nstatus: " + status + "\n---\n# " + id + ". Card\n"
	path := filepath.Join(dir, id+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%s): %v", path, err)
	}
	return path
}

func TestCardIndexStateChangesReuseOneScan(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 60; i++ {
		writeIndexCard(t, dir, "T-"+strconv.Itoa(i), "available")
	}
	store := CardStore{Dir: dir}
	resetCardIndex(dir)
	cardScanCount.Store(0)

	// The first state change builds the index.
	if err := store.indexedPatchStatus(context.Background(), "T-0", "in_progress"); err != nil {
		t.Fatalf("first patch: %v", err)
	}
	if got := cardScanCount.Load(); got != 1 {
		t.Fatalf("scans after first patch = %d, want 1", got)
	}

	// The indexed card is identical to a fresh parse of the file on disk.
	c, err := store.indexedFind("T-0")
	if err != nil {
		t.Fatalf("indexedFind(T-0): %v", err)
	}
	b, err := os.ReadFile(c.path)
	if err != nil {
		t.Fatalf("os.ReadFile(c.path): %v", err)
	}
	freshTask, ok, hasFM := parseCardFields(string(b), c.path)
	if !ok {
		t.Fatal("written card no longer parses")
	}
	fresh := card{path: c.path, task: freshTask, content: string(b), hasFM: hasFM}
	if !reflect.DeepEqual(c, fresh) {
		t.Fatalf("indexed card differs from fresh parse:\nindexed = %+v\nfresh   = %+v", c, fresh)
	}

	// Later state changes reuse the index and never walk the directory again.
	for i := 1; i < 30; i++ {
		id := core.TaskID("T-" + strconv.Itoa(i))
		if err := store.indexedPatchStatus(context.Background(), id, "done"); err != nil {
			t.Fatalf("patch %s: %v", id, err)
		}
	}
	if got := cardScanCount.Load(); got != 1 {
		t.Fatalf("scans after 30 state changes = %d, want 1", got)
	}
	for i := 0; i < 30; i++ {
		id := core.TaskID("T-" + strconv.Itoa(i))
		c, err := store.indexedFind(id)
		if err != nil {
			t.Fatalf("indexedFind(%s): %v", id, err)
		}
		if c.task.ID != id {
			t.Fatalf("card id = %q, want %q", c.task.ID, id)
		}
	}

	if err := store.indexedPatchStatus(context.Background(), "T-MISSING", "done"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing id error = %v, want os.ErrNotExist", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.indexedPatchStatus(ctx, "T-1", "done"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled patch error = %v, want context.Canceled", err)
	}
}

func TestCardIndexDetectsExternalEdit(t *testing.T) {
	dir := t.TempDir()
	writeIndexCard(t, dir, "T-X", "available")
	store := CardStore{Dir: dir}
	resetCardIndex(dir)

	c, err := store.indexedFind("T-X")
	if err != nil {
		t.Fatalf("indexedFind before edit: %v", err)
	}
	if c.task.Status != core.TaskAvailable {
		t.Fatalf("status = %q, want available", c.task.Status)
	}

	// An edit outside the store is picked up from mtime, without a full scan.
	time.Sleep(5 * time.Millisecond)
	path := filepath.Join(dir, "T-X.md")
	content := "---\nid: T-X\ntitle: Externally done\nstatus: done\n---\n# T-X. Card\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(path): %v", err)
	}
	before := cardScanCount.Load()
	c, err = store.indexedFind("T-X")
	if err != nil {
		t.Fatalf("indexedFind after edit: %v", err)
	}
	if c.task.Status != core.TaskDone || c.task.Title != "Externally done" {
		t.Fatalf("stale card after external edit: %+v", c.task)
	}
	if got := cardScanCount.Load(); got != before {
		t.Fatalf("external edit caused %d extra scans", got-before)
	}
}

func TestCardIndexStateChangesThroughMarkdown(t *testing.T) {
	dir := t.TempDir()
	writeIndexCard(t, dir, "T-MD", "available")
	s := NewMarkdown(dir)
	resetCardIndex(dir)
	cardScanCount.Store(0)

	if err := s.Claim(context.Background(), "T-MD"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := s.Complete(context.Background(), "T-MD"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "T-MD.md"))
	if err != nil {
		t.Fatalf("os.ReadFile: %v", err)
	}
	if !strings.Contains(string(b), "status: done") {
		t.Fatalf("Complete did not write done: %s", b)
	}
	if got := cardScanCount.Load(); got != 1 {
		t.Fatalf("scans after Claim and Complete = %d, want 1", got)
	}

	tasks, err := s.ListAvailable(context.Background())
	if err != nil {
		t.Fatalf("ListAvailable: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("completed card still available: %+v", tasks)
	}
}

// BenchmarkCardStoreStateChangesOneScan runs 500 state changes over 20,000
// cards. The index is built once per iteration and reused by every change.
// Each returned card must match a fresh parse of the file it came from.
func BenchmarkCardStoreStateChangesOneScan(b *testing.B) {
	ctx := context.Background()
	dir := b.TempDir()
	const totalCards = 20000
	for i := 0; i < totalCards; i++ {
		writeIndexCard(b, dir, "T-"+strconv.Itoa(i), "available")
	}
	store := CardStore{Dir: dir}
	ids := make([]core.TaskID, 500)
	for i := range ids {
		ids[i] = core.TaskID("T-" + strconv.Itoa(i))
	}
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		resetCardIndex(dir)
		cardScanCount.Store(0)
		for _, id := range ids {
			if err := store.indexedPatchStatus(ctx, id, "in_progress"); err != nil {
				b.Fatalf("patch %s: %v", id, err)
			}
		}
		if got := cardScanCount.Load(); got != 1 {
			b.Fatalf("full scans = %d, want 1", got)
		}
		for _, id := range ids {
			c, err := store.indexedFind(id)
			if err != nil {
				b.Fatalf("indexedFind(%s): %v", id, err)
			}
			bts, err := os.ReadFile(c.path)
			if err != nil {
				b.Fatalf("os.ReadFile(%s): %v", c.path, err)
			}
			t, ok, hasFM := parseCardFields(string(bts), c.path)
			if !ok || !reflect.DeepEqual(c, card{path: c.path, task: t, content: string(bts), hasFM: hasFM}) {
				b.Fatalf("indexed card differs from disk for %s", id)
			}
		}
	}
}

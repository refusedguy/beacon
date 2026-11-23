package tasksource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestMarkdownLegacyLifecycle(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "archive")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sub, "legacy-card.md")
	if err := os.WriteFile(path, []byte(legacyCard), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewMarkdown(dir)
	tasks, err := s.ListAvailable(context.Background())
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Статус: in_progress") {
		t.Fatalf("claim did not write in_progress: %s", b)
	}
	tasks, err = s.ListAvailable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("claimed task still available: %+v", tasks)
	}
	if err := s.Complete(context.Background(), "T-1"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), "Статус: done") {
		t.Fatalf("complete did not write done: %s", b)
	}
}

func TestMarkdownFrontmatter(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "infra", "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "frontmatter.md")
	if err := os.WriteFile(path, []byte(frontmatterCard), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewMarkdown(dir)
	tasks, err := s.ListAvailable(context.Background())
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
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

func TestMarkdownMarkInWorkspace(t *testing.T) {
	root := t.TempDir()
	cardDir := filepath.Join(root, "docs", "tasks")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatal(err)
	}
	card := "---\nid: T-9\ntitle: Workspace card\nstatus: available\n---\n# T-9. Workspace card\n"
	path := filepath.Join(cardDir, "T-9.md")
	if err := os.WriteFile(path, []byte(card), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewMarkdown(filepath.Join(root, "orchestrator-cards"))

	if err := s.MarkInWorkspace(context.Background(), "T-9", root, "in_progress"); err != nil {
		t.Fatalf("MarkInWorkspace(in_progress) error = %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
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

func TestScopeClassification(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
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

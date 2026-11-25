package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

const updateCardBody = `---
id: T-A
title: Old title
status: in_progress
scope:
  - a.go
depends:
  - T-X
priority: 5
kind: task
phase: design
---
# T-A. Card

Body line.
`

func writeUpdateCard(t *testing.T, dir, id, content string) string {
	t.Helper()
	cards := filepath.Join(dir, "docs", "tasks")
	if err := os.MkdirAll(cards, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cards, id+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readUpdateCard(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newUpdater(dir string, store ActiveRunFinder, ws PathCommitter) *TaskUpdater {
	return &TaskUpdater{Store: store, Workspace: ws, Root: core.Workspace{Path: dir, Branch: "main"}}
}

func TestTaskUpdaterPreservesFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := writeUpdateCard(t, dir, "T-A", updateCardBody)
	ws := &fakeWorkspace{log: &callLog{}}
	updater := newUpdater(dir, newFakeStore(), ws)
	title := "New title"

	got, err := updater.Update(context.Background(), []UpdateInput{{ID: "T-A", Title: &title}})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Pushed || len(got.Results) != 1 || got.Results[0].Status != "updated" {
		t.Fatalf("result = %+v", got)
	}

	updated := readUpdateCard(t, path)
	for _, want := range []string{
		"title: New title", "status: in_progress", "phase: design",
		"  - a.go", "  - T-X", "priority: 5", "kind: task", "Body line.",
	} {
		if !strings.Contains(updated, want) {
			t.Errorf("updated card lost %q:\n%s", want, updated)
		}
	}
	if strings.Contains(updated, "Old title") {
		t.Errorf("old title still present:\n%s", updated)
	}
	if len(ws.commits) != 1 || ws.commits[0].pathspec != "docs/tasks" ||
		ws.commits[0].message != "beacon: update tasks" {
		t.Fatalf("commits = %+v", ws.commits)
	}
	if len(ws.rebases) != 1 || ws.rebases[0] != "main" {
		t.Fatalf("rebases = %v", ws.rebases)
	}
}

func TestTaskUpdaterBatchCommitsOnce(t *testing.T) {
	dir := t.TempDir()
	a := writeUpdateCard(t, dir, "T-A", updateCardBody)
	b := writeUpdateCard(t, dir, "T-B", "---\nid: T-B\npriority: 1\n---\nbody\n")
	ws := &fakeWorkspace{log: &callLog{}}
	updater := newUpdater(dir, newFakeStore(), ws)
	title, priority := "Renamed", 9

	got, err := updater.Update(context.Background(), []UpdateInput{
		{ID: "T-A", Title: &title},
		{ID: "T-B", Priority: &priority},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Pushed || len(got.Results) != 2 ||
		got.Results[0].Status != "updated" || got.Results[1].Status != "updated" {
		t.Fatalf("result = %+v", got)
	}
	if !strings.Contains(readUpdateCard(t, a), "title: Renamed") ||
		!strings.Contains(readUpdateCard(t, b), "priority: 9") {
		t.Fatal("batch did not update both cards")
	}
	if len(ws.commits) != 1 {
		t.Fatalf("batch committed %d times", len(ws.commits))
	}
}

func TestTaskUpdaterReportsNotFoundWithoutPublishing(t *testing.T) {
	dir := t.TempDir()
	ws := &fakeWorkspace{log: &callLog{}}
	updater := newUpdater(dir, newFakeStore(), ws)

	got, err := updater.Update(context.Background(), []UpdateInput{{ID: "T-MISSING"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pushed || len(got.Results) != 1 || got.Results[0].Status != "not_found" {
		t.Fatalf("result = %+v", got)
	}
	if len(ws.commits) != 0 {
		t.Fatal("not_found still committed")
	}
}

func TestTaskUpdaterReportsUnchangedWithoutPublishing(t *testing.T) {
	dir := t.TempDir()
	path := writeUpdateCard(t, dir, "T-A", updateCardBody)
	ws := &fakeWorkspace{log: &callLog{}}
	updater := newUpdater(dir, newFakeStore(), ws)
	title := "Old title"

	got, err := updater.Update(context.Background(), []UpdateInput{{ID: "T-A", Title: &title}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pushed || len(got.Results) != 1 || got.Results[0].Status != "unchanged" {
		t.Fatalf("result = %+v", got)
	}
	if readUpdateCard(t, path) != updateCardBody {
		t.Fatal("unchanged card was rewritten")
	}
	if len(ws.commits) != 0 {
		t.Fatal("unchanged update still committed")
	}
}

func TestTaskUpdaterRefusesActiveRunBatch(t *testing.T) {
	dir := t.TempDir()
	a := writeUpdateCard(t, dir, "T-A", updateCardBody)
	b := writeUpdateCard(t, dir, "T-B", "---\nid: T-B\n---\nbody\n")
	store := newFakeStore()
	if err := store.Save(context.Background(), core.NewRun("run-1", "T-B", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	ws := &fakeWorkspace{log: &callLog{}}
	updater := newUpdater(dir, store, ws)
	title := "Should not land"

	got, err := updater.Update(context.Background(), []UpdateInput{
		{ID: "T-A", Title: &title}, {ID: "T-B", Title: &title},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Refused) != 1 || got.Refused[0] != "T-B" || got.Pushed {
		t.Fatalf("result = %+v", got)
	}
	if readUpdateCard(t, a) != updateCardBody ||
		!strings.Contains(readUpdateCard(t, b), "id: T-B") {
		t.Fatal("refusal touched the tree")
	}
	if len(ws.commits) != 0 {
		t.Fatal("refusal still committed")
	}
}

func TestTaskUpdaterRestoresCardsWhenPublishFails(t *testing.T) {
	dir := t.TempDir()
	path := writeUpdateCard(t, dir, "T-A", updateCardBody)
	ws := &failingCommitWorkspace{
		fakeWorkspace: &fakeWorkspace{log: &callLog{}},
		err:           errors.New("push failed"),
	}
	updater := newUpdater(dir, newFakeStore(), ws)
	title := "New title"

	if _, err := updater.Update(context.Background(), []UpdateInput{{ID: "T-A", Title: &title}}); !errors.Is(err, ws.err) {
		t.Fatalf("error = %v, want %v", err, ws.err)
	}
	if readUpdateCard(t, path) != updateCardBody {
		t.Fatal("failed publish did not restore the card")
	}
}

func TestTaskUpdaterReportsRollbackFailureWithPath(t *testing.T) {
	dir := t.TempDir()
	path := writeUpdateCard(t, dir, "T-A", updateCardBody)
	ws := &rollbackFailWorkspace{
		fakeWorkspace: &fakeWorkspace{log: &callLog{}},
		paths:         []string{path},
		err:           errors.New("push failed"),
	}
	updater := newUpdater(dir, newFakeStore(), ws)
	title := "New title"

	_, err := updater.Update(context.Background(), []UpdateInput{{ID: "T-A", Title: &title}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "push failed") {
		t.Fatalf("original error lost: %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("rollback error did not name %s: %v", path, err)
	}
}

func TestTaskUpdaterSyncsGraphOnceWithUpdatedFields(t *testing.T) {
	dir := t.TempDir()
	writeUpdateCard(t, dir, "T-A", updateCardBody)
	source := &cardSource{tasks: []core.Task{{ID: "T-A", Title: "Old title", Priority: 5}}}
	store := &syncStore{fakeStore: newFakeStore()}
	ws := &fakeWorkspace{log: &callLog{}}
	updater := &TaskUpdater{Store: store, Source: source, Workspace: ws, Root: core.Workspace{Path: dir, Branch: "main"}}
	title, priority := "Renamed", 9

	got, err := updater.Update(context.Background(), []UpdateInput{
		{ID: "T-A", Title: &title, Priority: &priority},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Pushed {
		t.Fatalf("result = %+v", got)
	}
	if source.calls != 1 || store.calls != 1 {
		t.Fatalf("source calls = %d, sync calls = %d", source.calls, store.calls)
	}
	if len(store.tasks) != 1 || store.tasks[0].Title != "Renamed" || store.tasks[0].Priority != 9 {
		t.Fatalf("synced graph = %+v", store.tasks)
	}
	if len(ws.commits) != 1 {
		t.Fatalf("commits = %d, want one push", len(ws.commits))
	}
}

func TestTaskUpdaterRestoresGraphWhenPublishFails(t *testing.T) {
	dir := t.TempDir()
	path := writeUpdateCard(t, dir, "T-A", updateCardBody)
	source := &cardSource{tasks: []core.Task{{ID: "T-A", Title: "Old title"}}}
	store := &syncStore{fakeStore: newFakeStore()}
	ws := &failingCommitWorkspace{fakeWorkspace: &fakeWorkspace{log: &callLog{}}, err: errors.New("push failed")}
	updater := &TaskUpdater{Store: store, Source: source, Workspace: ws, Root: core.Workspace{Path: dir, Branch: "main"}}
	title := "New title"

	if _, err := updater.Update(context.Background(), []UpdateInput{{ID: "T-A", Title: &title}}); !errors.Is(err, ws.err) {
		t.Fatalf("error = %v, want %v", err, ws.err)
	}
	if store.calls != 2 {
		t.Fatalf("sync calls = %d, want one sync and one restore", store.calls)
	}
	if len(store.tasks) != 1 || store.tasks[0].Title != "Old title" {
		t.Fatalf("restored graph = %+v", store.tasks)
	}
	if readUpdateCard(t, path) != updateCardBody {
		t.Fatal("failed publish did not restore the card")
	}
}

func TestTaskUpdaterUpdatesPlanAndBody(t *testing.T) {
	dir := t.TempDir()
	path := writeUpdateCard(t, dir, "T-A", "---\nid: T-A\n---\nold body\n")
	ws := &fakeWorkspace{log: &callLog{}}
	updater := newUpdater(dir, newFakeStore(), ws)
	body := "new body"
	plan := []core.ChildSpec{{
		Title:    "Child one",
		Scope:    []core.ScopePath{core.ScopeFile("a.go")},
		Depends:  []core.TaskID{"EXT-1"},
		Priority: 7,
	}}

	got, err := updater.Update(context.Background(), []UpdateInput{{
		ID: "T-A", Body: &body, Plan: &plan,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Pushed || got.Results[0].Status != "updated" {
		t.Fatalf("result = %+v", got)
	}
	updated := readUpdateCard(t, path)
	for _, want := range []string{"plan:", "- title: Child one", "- a.go", "- EXT-1", "priority: 7", "new body"} {
		if !strings.Contains(updated, want) {
			t.Errorf("updated card lost %q:\n%s", want, updated)
		}
	}
	if strings.Contains(updated, "old body") {
		t.Errorf("old body still present:\n%s", updated)
	}
}

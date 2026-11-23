package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

func writeCards(t *testing.T, dir string, ids ...string) {
	t.Helper()
	cards := filepath.Join(dir, "docs", "tasks")
	if err := os.MkdirAll(cards, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		body := "---\nid: " + id + "\n---\n# " + id + ". Card\n"
		if err := os.WriteFile(filepath.Join(cards, id+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func cardExists(t *testing.T, dir, id string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, "docs", "tasks", id+".md"))
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	t.Fatal(err)
	return false
}

func newDeleter(dir string, store ActiveRunFinder, ws PathCommitter) *TaskDeleter {
	return &TaskDeleter{Store: store, Workspace: ws, Root: core.Workspace{Path: dir, Branch: "main"}}
}

func TestTaskDeleterDeletesBatchInOneCommit(t *testing.T) {
	dir := t.TempDir()
	writeCards(t, dir, "T-A", "T-B", "T-KEEP")
	ws := &fakeWorkspace{log: &callLog{}}
	deleter := newDeleter(dir, newFakeStore(), ws)

	got, err := deleter.Delete(context.Background(), []string{"T-A", "T-B"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Pushed || len(got.Results) != 2 {
		t.Fatalf("result = %+v", got)
	}
	if got.Results[0].ID != "T-A" || got.Results[0].Status != "deleted" ||
		got.Results[1].ID != "T-B" || got.Results[1].Status != "deleted" {
		t.Fatalf("results = %+v", got.Results)
	}
	if cardExists(t, dir, "T-A") || cardExists(t, dir, "T-B") || !cardExists(t, dir, "T-KEEP") {
		t.Fatal("wrong cards removed")
	}
	if len(ws.commits) != 1 || ws.commits[0].pathspec != "docs/tasks" ||
		ws.commits[0].message != "beacon: delete tasks" {
		t.Fatalf("commits = %+v", ws.commits)
	}
	if len(ws.rebases) != 1 || ws.rebases[0] != "main" {
		t.Fatalf("rebases = %v", ws.rebases)
	}
}

func TestTaskDeleterIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	writeCards(t, dir, "T-A")
	ws := &fakeWorkspace{log: &callLog{}}
	deleter := newDeleter(dir, newFakeStore(), ws)
	ctx := context.Background()

	first, err := deleter.Delete(ctx, []string{"T-A"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := deleter.Delete(ctx, []string{"T-A"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Results[0].Status != "deleted" || second.Results[0].Status != "not_found" {
		t.Fatalf("first = %+v, second = %+v", first.Results, second.Results)
	}
	if second.Pushed || len(ws.commits) != 1 {
		t.Fatalf("second delete committed again: %+v", ws.commits)
	}
}

func TestTaskDeleterReportsNotFound(t *testing.T) {
	dir := t.TempDir()
	writeCards(t, dir, "T-A")
	ws := &fakeWorkspace{log: &callLog{}}
	deleter := newDeleter(dir, newFakeStore(), ws)

	got, err := deleter.Delete(context.Background(), []string{"T-MISSING"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 1 || got.Results[0].Status != "not_found" || got.Pushed {
		t.Fatalf("result = %+v", got)
	}
	if len(ws.commits) != 0 {
		t.Fatal("not_found delete still committed")
	}
}

func TestTaskDeleterRefusesActiveRunBatch(t *testing.T) {
	dir := t.TempDir()
	writeCards(t, dir, "T-A", "T-B")
	store := newFakeStore()
	if err := store.Save(context.Background(), core.NewRun("run-1", "T-B", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	ws := &fakeWorkspace{log: &callLog{}}
	deleter := newDeleter(dir, store, ws)

	got, err := deleter.Delete(context.Background(), []string{"T-A", "T-B"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Refused) != 1 || got.Refused[0] != "T-B" {
		t.Fatalf("refused = %v", got.Refused)
	}
	if len(got.Results) != 0 || got.Pushed {
		t.Fatalf("result = %+v", got)
	}
	if !cardExists(t, dir, "T-A") || !cardExists(t, dir, "T-B") {
		t.Fatal("refusal touched the tree")
	}
	if len(ws.commits) != 0 {
		t.Fatal("refusal still committed")
	}
}

func TestTaskDeleterRestoresCardsWhenPublishFails(t *testing.T) {
	dir := t.TempDir()
	writeCards(t, dir, "T-A", "T-B")
	want := errors.New("push failed")
	ws := &failingCommitWorkspace{fakeWorkspace: &fakeWorkspace{log: &callLog{}}, err: want}
	deleter := newDeleter(dir, newFakeStore(), ws)

	_, err := deleter.Delete(context.Background(), []string{"T-A", "T-B"})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if !cardExists(t, dir, "T-A") || !cardExists(t, dir, "T-B") {
		t.Fatal("failed publish did not restore cards")
	}
	b, err := os.ReadFile(filepath.Join(dir, "docs", "tasks", "T-A.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "---\nid: T-A\n---\n# T-A. Card\n" {
		t.Fatalf("restored content = %q", b)
	}
}

type failingCommitWorkspace struct {
	*fakeWorkspace
	err error
}

func (f *failingCommitWorkspace) CommitPath(ctx context.Context, ws core.Workspace, pathspec, message string) error {
	_ = f.fakeWorkspace.CommitPath(ctx, ws, pathspec, message)
	return f.err
}

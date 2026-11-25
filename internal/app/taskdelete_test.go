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

// rollbackFailWorkspace blocks every card path at the commit boundary so the
// rollback writes fail and must name the affected paths.
type rollbackFailWorkspace struct {
	*fakeWorkspace
	paths []string
	err   error
}

func (w *rollbackFailWorkspace) CommitPath(ctx context.Context, ws core.Workspace, pathspec, message string) error {
	for _, path := range w.paths {
		_ = os.RemoveAll(path)
		_ = os.MkdirAll(path, 0o755)
	}
	return w.err
}

// cardSource is a single-read task lister used to check the batch sync count.
type cardSource struct {
	tasks []core.Task
	err   error
	calls int
}

func (s *cardSource) ListAvailable(context.Context) ([]core.Task, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.tasks, nil
}

// syncStore counts graph writes and keeps the last graph it received.
type syncStore struct {
	*fakeStore
	tasks []core.Task
	calls int
	err   error
}

func (s *syncStore) SyncTasks(_ context.Context, tasks []core.Task) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	s.tasks = tasks
	return nil
}

func TestTaskDeleterReportsRollbackFailureWithPaths(t *testing.T) {
	dir := t.TempDir()
	writeCards(t, dir, "T-A", "T-B")
	paths := []string{
		filepath.Join(dir, "docs", "tasks", "T-A.md"),
		filepath.Join(dir, "docs", "tasks", "T-B.md"),
	}
	ws := &rollbackFailWorkspace{
		fakeWorkspace: &fakeWorkspace{log: &callLog{}},
		paths:         paths,
		err:           errors.New("push failed"),
	}
	deleter := newDeleter(dir, newFakeStore(), ws)

	_, err := deleter.Delete(context.Background(), []string{"T-A", "T-B"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "push failed") {
		t.Fatalf("original error lost: %v", err)
	}
	for _, path := range paths {
		if !strings.Contains(err.Error(), path) {
			t.Errorf("rollback error did not name %s: %v", path, err)
		}
	}
}

func TestTaskDeleterRollbackFailureNamesAffectedCard(t *testing.T) {
	dir := t.TempDir()
	writeCards(t, dir, "T-CARD")
	path := filepath.Join(dir, "docs", "tasks", "T-CARD.md")
	ws := &rollbackFailWorkspace{
		fakeWorkspace: &fakeWorkspace{log: &callLog{}},
		paths:         []string{path},
		err:           errors.New("push failed"),
	}
	deleter := newDeleter(dir, newFakeStore(), ws)

	_, err := deleter.Delete(context.Background(), []string{"T-CARD"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "push failed") {
		t.Fatalf("original error lost: %v", err)
	}
	if !strings.Contains(err.Error(), "rollback card T-CARD") {
		t.Fatalf("rollback error did not name the affected card: %v", err)
	}
}

func TestTaskDeleterSyncsGraphOnceWithoutDeletedCards(t *testing.T) {
	dir := t.TempDir()
	writeCards(t, dir, "T-A", "T-B", "T-KEEP")
	source := &cardSource{tasks: []core.Task{
		{ID: "T-A", Title: "A"}, {ID: "T-B", Title: "B"}, {ID: "T-KEEP", Title: "Keep"},
	}}
	store := &syncStore{fakeStore: newFakeStore()}
	ws := &fakeWorkspace{log: &callLog{}}
	deleter := &TaskDeleter{Store: store, Source: source, Workspace: ws, Root: core.Workspace{Path: dir, Branch: "main"}}

	if _, err := deleter.Delete(context.Background(), []string{"T-A", "T-B"}); err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || store.calls != 1 {
		t.Fatalf("source calls = %d, sync calls = %d", source.calls, store.calls)
	}
	if len(store.tasks) != 1 || store.tasks[0].ID != "T-KEEP" {
		t.Fatalf("synced graph = %+v", store.tasks)
	}
	if len(ws.commits) != 1 {
		t.Fatalf("commits = %d, want one push", len(ws.commits))
	}
}

func TestTaskDeleterRestoresGraphWhenPublishFails(t *testing.T) {
	dir := t.TempDir()
	writeCards(t, dir, "T-A")
	source := &cardSource{tasks: []core.Task{{ID: "T-A", Title: "A"}}}
	store := &syncStore{fakeStore: newFakeStore()}
	ws := &failingCommitWorkspace{fakeWorkspace: &fakeWorkspace{log: &callLog{}}, err: errors.New("push failed")}
	deleter := &TaskDeleter{Store: store, Source: source, Workspace: ws, Root: core.Workspace{Path: dir, Branch: "main"}}

	if _, err := deleter.Delete(context.Background(), []string{"T-A"}); !errors.Is(err, ws.err) {
		t.Fatalf("error = %v, want %v", err, ws.err)
	}
	if store.calls != 2 {
		t.Fatalf("sync calls = %d, want one sync and one restore", store.calls)
	}
	if len(store.tasks) != 1 || store.tasks[0].ID != "T-A" {
		t.Fatalf("restored graph = %+v", store.tasks)
	}
	if !cardExists(t, dir, "T-A") {
		t.Fatal("failed publish did not restore the card")
	}
}

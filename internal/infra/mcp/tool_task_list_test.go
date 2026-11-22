package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

// fakeTaskListStore records the normalized input so handler tests can assert
// the default limit and the phase filter without touching the filesystem.
type fakeTaskListStore struct {
	rows      []TaskListCard
	err       error
	gotInput  TaskListInput
	callCount int
}

func (f *fakeTaskListStore) Available(_ context.Context, in TaskListInput) ([]TaskListCard, error) {
	f.gotInput = in
	f.callCount++
	return f.rows, f.err
}

func listTestRows(n int) []TaskListCard {
	rows := make([]TaskListCard, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, TaskListCard{ID: fmt.Sprintf("LST-%02d", i), Phase: "P1"})
	}
	return rows
}

func TestTaskListHandlerDefaultLimit(t *testing.T) {
	store := &fakeTaskListStore{rows: listTestRows(13)}
	h := NewTaskListHandler(store)

	out, err := h.List(context.Background(), TaskListInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if store.gotInput.Limit != defaultTaskListLimit {
		t.Fatalf("store limit = %d, want %d", store.gotInput.Limit, defaultTaskListLimit)
	}
	if len(out.Cards) != defaultTaskListLimit || !out.Truncated {
		t.Fatalf("got %d cards, truncated=%v; want %d and true", len(out.Cards), out.Truncated, defaultTaskListLimit)
	}
}

func TestTaskListHandlerExplicitLimit(t *testing.T) {
	store := &fakeTaskListStore{rows: listTestRows(3)}
	h := NewTaskListHandler(store)

	out, err := h.List(context.Background(), TaskListInput{Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out.Cards) != 2 || !out.Truncated {
		t.Fatalf("got %d cards, truncated=%v; want 2 and true", len(out.Cards), out.Truncated)
	}

	out, err = h.List(context.Background(), TaskListInput{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out.Cards) != 3 || out.Truncated {
		t.Fatalf("got %d cards, truncated=%v; want 3 and false", len(out.Cards), out.Truncated)
	}
}

func TestTaskListHandlerValidatesPhase(t *testing.T) {
	store := &fakeTaskListStore{}
	h := NewTaskListHandler(store)

	_, err := h.List(context.Background(), TaskListInput{Phase: "P9"})
	if claimErrorCode(t, err) != domain.CodeInvalidInput {
		t.Fatalf("bad phase gave %v, want INVALID_INPUT", err)
	}
	if store.callCount != 0 {
		t.Fatalf("store called %d times for an invalid phase", store.callCount)
	}

	if _, err = h.List(context.Background(), TaskListInput{Phase: " P2 "}); err != nil {
		t.Fatalf("trimmed valid phase: %v", err)
	}
	if store.gotInput.Phase != "P2" {
		t.Fatalf("store phase = %q, want P2", store.gotInput.Phase)
	}
}

func TestTaskListHandlerRequiresStore(t *testing.T) {
	_, err := NewTaskListHandler(nil).List(context.Background(), TaskListInput{})
	if claimErrorCode(t, err) != domain.CodePersistFailed {
		t.Fatalf("nil store gave %v, want PERSIST_FAILED", err)
	}
}

func TestTaskListHandlerPassesStoreError(t *testing.T) {
	want := errors.New("index down")
	_, err := NewTaskListHandler(&fakeTaskListStore{err: want}).List(context.Background(), TaskListInput{})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func listTestCard(id, phase, status, agent, ownerFiles, goal string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s. Card %s\n", id, id)
	fmt.Fprintf(&b, "Статус: %s\n", status)
	fmt.Fprintf(&b, "Фаза: %s\n", phase)
	fmt.Fprintln(&b, "Тип: task")
	fmt.Fprintf(&b, "Владелец файлов: %s\n", ownerFiles)
	fmt.Fprintln(&b, "Зависимости: нет")
	fmt.Fprintf(&b, "Агент: %s\n", agent)
	fmt.Fprintln(&b, "Время: нет")
	fmt.Fprintf(&b, "Цель: %s\n", goal)
	fmt.Fprintln(&b, "Контекст: контекст")
	fmt.Fprintln(&b, "Шаги: шаги")
	fmt.Fprintln(&b, "Проверка: проверка")
	fmt.Fprintln(&b, "Приёмка: приёмка")
	return b.String()
}

func writeListTestCard(t *testing.T, dir, id, phase, status, agent, ownerFiles, goal string) {
	t.Helper()
	text := listTestCard(id, phase, status, agent, ownerFiles, goal)
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(text), 0644); err != nil {
		t.Fatalf("write card %s: %v", id, err)
	}
}

func listTestLister(t *testing.T) *ClaimStoreTaskLister {
	t.Helper()
	dir := t.TempDir()
	longGoal := strings.Repeat("д", 200)
	writeListTestCard(t, dir, "LST-01", "P2", "todo", "нет", "internal/p2.go", "second goal")
	writeListTestCard(t, dir, "LST-02", "P0", "todo", "нет", "internal/p0.go", longGoal)
	writeListTestCard(t, dir, "LST-03", "P1", "todo", "нет", "internal/p1.go", "first goal")
	writeListTestCard(t, dir, "LST-04", "P1", "todo", "ses_other", "internal/claimed.go", "claimed goal")
	writeListTestCard(t, dir, "LST-05", "P1", "in_progress", "нет", "internal/busy.go", "busy goal")
	writeListTestCard(t, dir, "LST-06", "P3", "todo", "нет", "internal/excluded.go", "excluded goal")
	return &ClaimStoreTaskLister{Claim: &FileTaskClaimStore{TasksDir: dir, StateDir: dir}}
}

func TestClaimStoreTaskListerFiltersAndOrders(t *testing.T) {
	lister := listTestLister(t)
	h := NewTaskListHandler(lister)

	out, err := h.List(context.Background(), TaskListInput{
		ExcludeOwnerFiles: []string{"internal/excluded.go"},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := make([]string, 0, len(out.Cards))
	for _, card := range out.Cards {
		got = append(got, card.ID)
		if card.Status != "todo" || card.Agent != "" {
			t.Fatalf("card %s: status=%q agent=%q; want todo and empty", card.ID, card.Status, card.Agent)
		}
	}
	want := []string{"LST-02", "LST-03", "LST-01"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if out.Truncated {
		t.Fatal("three cards should not be truncated by the default limit")
	}

	first := out.Cards[0]
	if first.Phase != "P0" || len(first.OwnerFiles) != 1 || first.OwnerFiles[0] != "internal/p0.go" {
		t.Fatalf("first card = %+v, want P0 with its owner file", first)
	}
	if runes := len([]rune(first.Goal)); runes != taskListGoalRunes {
		t.Fatalf("goal preview = %d runes, want %d", runes, taskListGoalRunes)
	}
}

func TestClaimStoreTaskListerPhaseFilter(t *testing.T) {
	h := NewTaskListHandler(listTestLister(t))

	out, err := h.List(context.Background(), TaskListInput{Phase: "P1"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out.Cards) != 1 || out.Cards[0].ID != "LST-03" {
		t.Fatalf("cards = %+v, want only LST-03", out.Cards)
	}
}

func TestClaimStoreTaskListerRejectsEmptyDir(t *testing.T) {
	_, err := NewTaskListHandler(&ClaimStoreTaskLister{Claim: &FileTaskClaimStore{}}).List(context.Background(), TaskListInput{})
	if claimErrorCode(t, err) != domain.CodePersistFailed {
		t.Fatalf("empty tasks dir gave %v, want PERSIST_FAILED", err)
	}
}

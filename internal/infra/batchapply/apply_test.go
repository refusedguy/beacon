package batchapply

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// todoCard mirrors the shape rendered by renderCard. Only the status line and
// the trailing newline matter for a closure, but keeping a realistic card
// makes a broken format easy to spot in a failure message.
const todoCard = "# W5-42. Пример карточки\n" +
	"Статус: todo\n" +
	"Фаза: P3\n" +
	"Тип: task\n" +
	"Владелец файлов: internal/infra/batchapply/apply.go\n" +
	"Зависимости: \n" +
	"Ссылка: \n" +
	"Агент: нет\n" +
	"Время: нет\n" +
	"Цель: проверить закрытие\n" +
	"Контекст: \n" +
	"Шаги: \n" +
	"Проверка: \n" +
	"Приёмка: \n"

// TestApplyClosureToCardStatuses proves that done, empty and unknown values
// keep the original output while dropped changes the status line and records
// the reason in the confirmation section.
func TestApplyClosureToCardStatuses(t *testing.T) {
	cases := []struct {
		name       string
		closure    cardClosure
		wantStatus string
		wantLine   string
		reject     []string
	}{
		{
			name:       "empty status keeps done behaviour",
			closure:    cardClosure{PR: 101, MergedAt: "2026-11-20"},
			wantStatus: "Статус: done",
			wantLine:   "- PR #101 MERGED: 2026-11-20 (squash).",
			reject:     []string{"DROPPED"},
		},
		{
			name:       "explicit done keeps done behaviour",
			closure:    cardClosure{PR: 101, Status: "done", MergedAt: "2026-11-20"},
			wantStatus: "Статус: done",
			wantLine:   "- PR #101 MERGED: 2026-11-20 (squash).",
			reject:     []string{"DROPPED"},
		},
		{
			name:       "unknown status defaults to done",
			closure:    cardClosure{PR: 101, Status: "closed", MergedAt: "2026-11-20"},
			wantStatus: "Статус: done",
			wantLine:   "- PR #101 MERGED: 2026-11-20 (squash).",
			reject:     []string{"DROPPED"},
		},
		{
			name:       "dropped records the reason",
			closure:    cardClosure{PR: 102, Status: "dropped", Reason: "дублирует W3-A"},
			wantStatus: "Статус: dropped",
			wantLine:   "- PR #102 DROPPED: дублирует W3-A.",
			reject:     []string{"Статус: todo", "MERGED"},
		},
		{
			name:       "dropped without a reason stays well formed",
			closure:    cardClosure{PR: 103, Status: "dropped"},
			wantStatus: "Статус: dropped",
			wantLine:   "- PR #103 DROPPED.",
			reject:     []string{"Статус: todo", "MERGED"},
		},
		{
			name:       "trailing dot in the reason is not doubled",
			closure:    cardClosure{PR: 104, Status: "dropped", Reason: "дублирует W3-A."},
			wantStatus: "Статус: dropped",
			wantLine:   "- PR #104 DROPPED: дублирует W3-A.",
			reject:     []string{".."},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applyClosureToCard(todoCard, tc.closure)
			if !strings.Contains(got, tc.wantStatus+"\n") {
				t.Fatalf("card missing status %q:\n%s", tc.wantStatus, got)
			}
			if !strings.Contains(got, "\n## Подтверждение\n") {
				t.Fatalf("card missing confirmation section:\n%s", got)
			}
			if !strings.Contains(got, tc.wantLine) {
				t.Fatalf("card missing confirmation line %q:\n%s", tc.wantLine, got)
			}
			for _, bad := range tc.reject {
				if strings.Contains(got, bad) {
					t.Fatalf("card unexpectedly contains %q:\n%s", bad, got)
				}
			}
			if !strings.HasSuffix(got, "\n") {
				t.Fatalf("card must keep its trailing newline:\n%s", got)
			}
		})
	}
}

// TestApplyClosureToCardIdempotent proves that applying the same drop twice
// neither duplicates the reason nor rewrites the status line.
func TestApplyClosureToCardIdempotent(t *testing.T) {
	c := cardClosure{PR: 105, Status: "dropped", Reason: "дублирует W3-A"}
	once := applyClosureToCard(todoCard, c)
	twice := applyClosureToCard(once, c)
	if twice != once {
		t.Fatalf("second application changed the card:\nfirst:\n%s\nsecond:\n%s", once, twice)
	}
	for _, check := range []struct {
		what   string
		needle string
		want   int
	}{
		{"reason", "дублирует W3-A", 1},
		{"confirmation section", "## Подтверждение", 1},
		{"dropped status", "Статус: dropped", 1},
	} {
		if n := strings.Count(twice, check.needle); n != check.want {
			t.Fatalf("%s appears %d times, want %d:\n%s", check.what, n, check.want, twice)
		}
	}
}

// TestApplyClosuresWritesAndIsIdempotent covers the real file path: the card
// on disk is rewritten once, and a second run leaves it byte-for-byte equal.
func TestApplyClosuresWritesAndIsIdempotent(t *testing.T) {
	for _, status := range []string{"", "done", "dropped"} {
		t.Run("status="+status, func(t *testing.T) {
			root := t.TempDir()
			rel := filepath.Join("docs", "tasks", "W5-42.md")
			path := filepath.Join(root, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(todoCard), 0o644); err != nil {
				t.Fatal(err)
			}

			r := &runner{out: io.Discard, err: io.Discard}
			c := cardClosure{
				ID: "W5-42", PR: 106, Status: status,
				Reason: "дублирует W3-A", MergedAt: "2026-11-20",
			}
			closures := map[string]cardClosure{c.ID: c}
			if err := r.applyClosures(root, closures); err != nil {
				t.Fatal(err)
			}
			first, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.applyClosures(root, closures); err != nil {
				t.Fatal(err)
			}
			second, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(first) != string(second) {
				t.Fatalf("second apply changed the file:\nfirst:\n%s\nsecond:\n%s", first, second)
			}

			wantStatus, wantLine, wantReasons := "Статус: done", "- PR #106 MERGED: 2026-11-20 (squash).", 0
			if status == "dropped" {
				wantStatus, wantLine, wantReasons = "Статус: dropped", "- PR #106 DROPPED: дублирует W3-A.", 1
			}
			if !strings.Contains(string(second), wantStatus+"\n") {
				t.Fatalf("file missing status %q:\n%s", wantStatus, second)
			}
			if !strings.Contains(string(second), wantLine) {
				t.Fatalf("file missing confirmation line %q:\n%s", wantLine, second)
			}
			if n := strings.Count(string(second), "дублирует W3-A"); n != wantReasons {
				t.Fatalf("reason appears %d times, want %d:\n%s", n, wantReasons, second)
			}
		})
	}
}

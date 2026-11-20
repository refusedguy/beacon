package batchapply

import (
	"fmt"
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

// TestReportFormatContract pins the exact report lines batch-apply prints
// (HD-039). Each line carries only fields in a fixed order. A label prefix,
// trailing prose or an arrow for a word adds extra words and breaks the style.
func TestReportFormatContract(t *testing.T) {
	cases := []struct {
		name   string
		format string
		args   []any
		want   string
	}{
		{"check", checkReportFormat, []any{3, 1}, "valid=3 invalid=1\n"},
		{"summary", summaryReportFormat, []any{2, 3, 4, 5, 6}, "batches=2 adrs=3 cards=4 evals=5 closures=6\n"},
		{"merge", mergeReportFormat, []any{2, 1}, "merged=2 held=1\n"},
		{"renumber", renumberReportFormat, []any{7, 265}, "renumber ADR 7 to 265\n"},
		{"skip", skipReportFormat, []any{"bad.json: invalid JSON"}, "skip bad.json: invalid JSON\n"},
		{"warn", warnReportFormat, []any{"docs/tasks/W5-42.md"}, "warn closure target missing: docs/tasks/W5-42.md\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fmt.Sprintf(tc.format, tc.args...)
			if got != tc.want {
				t.Fatalf("report format changed: got %q, want %q", got, tc.want)
			}
			if verboseReport(got) {
				t.Fatalf("report adds extra words: %q", got)
			}
		})
	}

	// Negative control: the old verbose lines carry labels, filler and an
	// arrow. The style check must reject every one of them.
	for _, old := range []string{
		"check: valid=3 invalid=1 (1 skipped)\n",
		"merge-plan: merged=2 held=1\n",
		"renumber ADR 7 -> 265\n",
		"SKIP bad.json: invalid JSON\n",
		"WARN closure target missing: docs/tasks/W5-42.md\n",
	} {
		if !verboseReport(old) {
			t.Fatalf("verbose report was not rejected: %q", old)
		}
	}
}

// verboseReport reports whether a line breaks the concise style from HD-039:
// a label prefix, an arrow in place of a word, or prose in parentheses.
func verboseReport(line string) bool {
	line = strings.TrimSuffix(line, "\n")
	for _, prefix := range []string{"check:", "merge-plan:", "SKIP ", "WARN "} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return strings.Contains(line, "->") || strings.Contains(line, " (")
}

// TestCheckReportOutput proves the real Run path prints the concise report:
// counts on stdout, the skipped file on stderr, and exit code 1 on bad input.
func TestCheckReportOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	code := Run(Options{Dir: dir, Check: true}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if got, want := out.String(), "valid=1 invalid=1\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := errOut.String(); !strings.HasPrefix(got, "skip bad.json: invalid JSON") {
		t.Fatalf("stderr = %q, want a concise skip line", got)
	}
}

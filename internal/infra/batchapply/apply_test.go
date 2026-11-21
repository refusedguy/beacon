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
// (HD-039 for style, HD-053 for language). Structured lines carry English keys
// in a fixed order. Prose lines address the operator in Russian. A label
// prefix, trailing prose or an arrow for a word adds extra words and breaks the
// style.
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
		{"renumber", renumberReportFormat, []any{7, 265}, "перенумеровать ADR 7 в 265\n"},
		{"skip", skipReportFormat, []any{"bad.json: неверный JSON"}, "пропуск bad.json: неверный JSON\n"},
		{"warn", warnReportFormat, []any{"docs/tasks/W5-42.md"}, "предупреждение: не найдена цель закрытия docs/tasks/W5-42.md\n"},
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
	if got := errOut.String(); !strings.HasPrefix(got, "пропуск bad.json: неверный JSON") {
		t.Fatalf("stderr = %q, want a concise Russian skip line", got)
	}
}

// TestLanguagePolicy pins the declared languages and checks that report lines
// use the language of their scope. Interface prose is Russian. Report keys and
// technical tokens stay English.
func TestLanguagePolicy(t *testing.T) {
	for _, tc := range []struct {
		scope languageScope
		want  string
	}{
		{scopeInterface, "ru"},
		{scopeArtifacts, "ru"},
		{scopeTechnical, "en"},
		{"unknown", ""},
	} {
		if got := languageFor(tc.scope); got != tc.want {
			t.Fatalf("languageFor(%q) = %q, want %q", tc.scope, got, tc.want)
		}
	}

	prose := []string{
		fmt.Sprintf(renumberReportFormat, 7, 265),
		fmt.Sprintf(skipReportFormat, "bad.json: неверный JSON"),
		fmt.Sprintf(warnReportFormat, "docs/tasks/W5-42.md"),
	}
	for _, line := range prose {
		if !hasCyrillic(line) {
			t.Fatalf("interface report line has no Russian prose: %q", line)
		}
	}

	structured := []string{
		fmt.Sprintf(checkReportFormat, 3, 1),
		fmt.Sprintf(summaryReportFormat, 2, 3, 4, 5, 6),
		fmt.Sprintf(mergeReportFormat, 2, 1),
	}
	for _, line := range structured {
		if hasCyrillic(line) {
			t.Fatalf("technical report key line was translated: %q", line)
		}
	}
}

// TestArtifactLanguagePolicy checks that generated documents use Russian
// headings and labels while identifiers and status values keep their English
// spelling. A wrong-language heading is the mixing the card rejects.
func TestArtifactLanguagePolicy(t *testing.T) {
	adrs := renderADR(adr{
		Number: 7, Title: "Решение", Context: "контекст",
		Decision: "выбор", Consequences: "последствие",
	})
	cardDoc := renderCard(card{ID: "HD-999", Title: "Пример", Status: "todo", Goal: "цель"})
	evalDoc := renderEval(evalCase{
		Name: "E1", Scenario: "сценарий",
		Assertions: []string{"проверка"}, NegativeControl: "контроль",
	})

	cases := []struct {
		name      string
		got       string
		russian   []string
		english   []string
		technical []string
	}{
		{
			name:      "adr",
			got:       adrs,
			russian:   []string{"Статус:", "## Контекст", "## Решение", "## Последствия"},
			english:   []string{"Status:", "## Context", "## Decision", "## Consequences"},
			technical: []string{"ADR 7", "accepted"},
		},
		{
			name:      "card",
			got:       cardDoc,
			russian:   []string{"Статус:", "Фаза:", "Тип:", "Владелец файлов:", "Цель:", "Приёмка:"},
			english:   []string{"Status:", "Phase:", "Type:", "Owner files:", "Goal:", "Acceptance:"},
			technical: []string{"HD-999", "todo", "P3", "task"},
		},
		{
			name:      "eval",
			got:       evalDoc,
			russian:   []string{"## Сценарий", "## Проверки", "## Негативный контроль"},
			english:   []string{"## Scenario", "## Checks", "## Negative control"},
			technical: []string{"E1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, needle := range tc.russian {
				if !strings.Contains(tc.got, needle) {
					t.Fatalf("%s missing Russian label %q:\n%s", tc.name, needle, tc.got)
				}
			}
			for _, needle := range tc.english {
				if strings.Contains(tc.got, needle) {
					t.Fatalf("%s uses English prose label %q:\n%s", tc.name, needle, tc.got)
				}
			}
			for _, needle := range tc.technical {
				if !strings.Contains(tc.got, needle) {
					t.Fatalf("%s lost technical token %q:\n%s", tc.name, needle, tc.got)
				}
			}
		})
	}
}

// hasCyrillic reports whether s contains a Russian letter. It keeps the
// language tests independent of any specific report wording.
func hasCyrillic(s string) bool {
	for _, r := range s {
		if (r >= 'а' && r <= 'я') || (r >= 'А' && r <= 'Я') || r == 'ё' || r == 'Ё' {
			return true
		}
	}
	return false
}

// TestEvaluateChecksMergeGate pins the auto-merge gate. A doc-only PR has no
// checks and passes. A code PR must carry every required check in a green
// state. Missing and non-green checks are held with different reasons, and
// GitHub's skipped and neutral states do not count as green. Every reported
// check is inspected, so a green group cannot hide a neutral run.
// TestCheckRejectsInvalidCardType proves --check fails closed on a Type value
// outside the generated card's closed set. The batch is checked before any
// artifact is written, so an invalid Type cannot reach docs/tasks. The valid
// case is the control: the same run with "task" exits zero.
func TestCheckRejectsInvalidCardType(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cardType   string
		wantCode   int
		wantStderr string
		wantStdout string
	}{
		{
			name: "spike is rejected", cardType: "spike", wantCode: 1,
			wantStderr: "недопустимое значение поля type: spike\n",
			// Validation runs before the check report, so stdout stays empty.
		},
		{
			name: "task is accepted", cardType: "task", wantCode: 0,
			wantStdout: "valid=1 invalid=0\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			raw := fmt.Sprintf(`{"cards":[{"id":"Q-A4-CARD","type":%q}]}`, tc.cardType)
			if err := os.WriteFile(filepath.Join(dir, "batch.json"), []byte(raw), 0o644); err != nil {
				t.Fatal(err)
			}

			var out, errOut strings.Builder
			code := Run(Options{Dir: dir, Check: true}, &out, &errOut)
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (stdout=%q stderr=%q)", code, tc.wantCode, out.String(), errOut.String())
			}
			if tc.wantStderr != "" && errOut.String() != tc.wantStderr {
				t.Fatalf("stderr = %q, want %q", errOut.String(), tc.wantStderr)
			}
			if got := out.String(); got != tc.wantStdout {
				t.Fatalf("stdout = %q, want %q", got, tc.wantStdout)
			}
			for _, rel := range []string{
				filepath.Join("docs", "tasks", "Q-A4-CARD.md"),
				filepath.Join("docs", "adrs", "README.md"),
				filepath.Join("docs", "FACTS.md"),
			} {
				if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
					t.Fatalf("--check wrote %s", rel)
				}
			}
		})
	}
}

// TestCardFieldProblemsCoversAllFields pins the closed sets for Тип, Фаза and
// Статус. An empty value is a default and stays valid; a declared value
// outside its set is reported once with its field name.
func TestCardFieldProblemsCoversAllFields(t *testing.T) {
	batches := []batch{{Cards: []card{
		{ID: "A", Type: "spike", Phase: "P9", Status: "closed"},
		{ID: "B", Type: "task", Phase: "P0", Status: "todo"},
		{ID: "C"},
	}}}
	got := cardFieldProblems(batches)
	want := []string{
		"недопустимое значение поля type: spike\n",
		"недопустимое значение поля phase: P9\n",
		"недопустимое значение поля status: closed\n",
	}
	if len(got) != len(want) {
		t.Fatalf("problems = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("problem %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestEvaluateChecksMergeGate(t *testing.T) {
	allGreen := []ghCheck{
		{Name: "build", State: "SUCCESS"},
		{Name: "test", State: "SUCCESS"},
		{Name: "gates", State: "SUCCESS"},
		{Name: "gates-binary", State: "SUCCESS"},
	}
	withState := func(name, state string) []ghCheck {
		out := append([]ghCheck(nil), allGreen...)
		for i := range out {
			if out[i].Name == name {
				out[i].State = state
			}
		}
		return out
	}

	cases := []struct {
		name   string
		checks []ghCheck
		want   string
	}{
		{"doc-only PR reports no checks", nil, ""},
		{"empty check list passes", []ghCheck{}, ""},
		{"all required checks green", allGreen, ""},
		{"lowercase success is green", []ghCheck{
			{Name: "build", State: "success"},
			{Name: "test", State: "PASS"},
			{Name: "gates", State: "success"},
			{Name: "gates-binary", State: "pass"},
		}, ""},
		{"missing required check is held", []ghCheck{
			{Name: "build", State: "SUCCESS"},
			{Name: "test", State: "SUCCESS"},
			{Name: "gates", State: "SUCCESS"},
		}, holdMissingCheck},
		{"only non-required checks is held", []ghCheck{
			{Name: "e2e", State: "SUCCESS"},
		}, holdMissingCheck},
		{"neutral required check is held", withState("gates", "NEUTRAL"), holdNotGreen},
		{"skipped required check is held", withState("test", "SKIPPED"), holdNotGreen},
		{"failed required check is held", withState("build", "FAILURE"), holdNotGreen},
		{"green group cannot hide a neutral run", append(withState("gates", "SUCCESS"),
			ghCheck{Name: "gates", State: "NEUTRAL"}), holdNotGreen},
		{"non-required failure does not block", append(append([]ghCheck(nil), allGreen...),
			ghCheck{Name: "e2e", State: "FAILURE"}), ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := evaluateChecks(tc.checks); got != tc.want {
				t.Fatalf("evaluateChecks() = %q, want %q", got, tc.want)
			}
		})
	}
}

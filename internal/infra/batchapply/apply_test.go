package batchapply

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestCardFieldProblemsAcceptsCanonicalStatuses proves every status from the
// taskstore canon passes cardFieldProblems. An existing batch with an
// in_progress, blocked or duplicate card must validate. A value outside the
// canon still produces one invalidField line.
func TestCardFieldProblemsAcceptsCanonicalStatuses(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   []string
	}{
		{status: "todo"},
		{status: "in_progress"},
		{status: "blocked"},
		{status: "done"},
		{status: "dropped"},
		{status: "duplicate"},
		{status: "closed", want: []string{"недопустимое значение поля status: closed\n"}},
	} {
		t.Run(tc.status, func(t *testing.T) {
			batches := []batch{{Cards: []card{
				{ID: "A", Type: "task", Phase: "P0", Status: tc.status},
			}}}
			got := cardFieldProblems(batches)
			if len(got) != len(tc.want) {
				t.Fatalf("problems = %q, want %q", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("problem %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestAllowedCardStatusesBaseline compares the local copy with the taskstore
// canon measured at six statuses. A change to either set must update this
// baseline, so the divergence becomes visible without importing
// internal/infra/taskstore across the layer boundary.
func TestAllowedCardStatusesBaseline(t *testing.T) {
	baseline := []string{"todo", "in_progress", "blocked", "done", "dropped", "duplicate"}
	if len(allowedCardStatuses) != len(baseline) {
		t.Fatalf("allowedCardStatuses = %q (%d values), want %d: %q", allowedCardStatuses, len(allowedCardStatuses), len(baseline), baseline)
	}
	for i := range baseline {
		if allowedCardStatuses[i] != baseline[i] {
			t.Fatalf("allowedCardStatuses[%d] = %q, want %q", i, allowedCardStatuses[i], baseline[i])
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

// TestCommitArtifactsSkipsIgnoredDerivedDocs covers IDX-11. Derived documents
// are gitignored, and naming one in git add makes batch-apply --commit exit 1.
// Staging the source trees by directory must commit ADRs, cards and evals while
// leaving every ignored document out of the commit.
func TestCommitArtifactsSkipsIgnoredDerivedDocs(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	git("config", "user.email", "batch@example.com")
	git("config", "user.name", "batch")
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(".gitignore", "docs/FACTS.md\ndocs/ROADMAP_STATUS.md\ndocs/adrs/README.md\n")
	write("docs/adrs/0001-source.md", "# ADR 1\n")
	write("docs/tasks/W1.md", "# W1\n")
	write("docs/evals/e1.md", "# e1\n")
	write("docs/FACTS.md", "derived\n")
	write("docs/adrs/README.md", "derived\n")
	write("docs/ROADMAP_STATUS.md", "derived\n")

	if err := commitArtifacts(root, 1, 1, 1); err != nil {
		t.Fatalf("commitArtifacts: %v", err)
	}
	committed := git("ls-tree", "-r", "--name-only", "HEAD")
	for _, want := range []string{"docs/adrs/0001-source.md", "docs/tasks/W1.md", "docs/evals/e1.md"} {
		if !strings.Contains(committed, want) {
			t.Fatalf("commit missing %q:\n%s", want, committed)
		}
	}
	for _, ignored := range []string{"docs/FACTS.md", "docs/adrs/README.md", "docs/ROADMAP_STATUS.md"} {
		if strings.Contains(committed, ignored) {
			t.Fatalf("commit contains ignored %q:\n%s", ignored, committed)
		}
	}
}

// TestWriteFileNeverOverwritesExistingCard pins the invariant that a later
// batch cannot replace a card already on disk. The commit change must not
// weaken it.
func TestWriteFileNeverOverwritesExistingCard(t *testing.T) {
	root := t.TempDir()
	rel := filepath.Join("docs", "tasks", "W1.md")
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const existing = "# W1. Existing card\nСтатус: done\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &runner{out: io.Discard, err: io.Discard}
	if err := r.writeFile(root, rel, "# W1. Replacement\n"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != existing {
		t.Fatalf("existing card changed: got %q, want %q", got, existing)
	}
}

// BVF-04. production-валидатор в apply.go уже отклоняет невалидные type,
// phase, status и id. Здесь проверяются два недостающих правила owner_file:
// файл обязан существовать в дереве и не может быть закреплён за двумя
// карточками одной фазы. Generated-документы FACTS.md и adrs/README.md
// остаются исключением: они пересобираются из исходников и не лежат в git.
const (
	missingOwnerFileFormat = "несуществующий owner_file: карточка %s, фаза %s, файл %s\n"
	overlapOwnerFileFormat = "пересечение owner_files в фазе %s: файл %s, карточки %s и %s\n"
)

// batchPackProblems объединяет существующую проверку полей с проверкой
// owner_files. Порядок детерминирован: batchы идут в порядке загрузки,
// карточки и файлы в порядке объявления. root задаёт каталог дерева, в котором
// ищутся owner_files.
func batchPackProblems(root string, batches []batch) []string {
	problems := append([]string(nil), cardFieldProblems(batches)...)
	problems = append(problems, cardIDProblems(batches)...)
	seen := map[string]string{}
	for _, b := range batches {
		for _, c := range b.Cards {
			for _, owner := range c.OwnerFiles {
				owner = filepath.ToSlash(strings.TrimSpace(owner))
				if owner == "" || derivedArtifact(owner) {
					continue
				}
				clean := filepath.Clean(owner)
				if filepath.IsAbs(owner) || clean == ".." || strings.HasPrefix(clean, "../") {
					problems = append(problems, fmt.Sprintf(missingOwnerFileFormat, c.ID, c.Phase, owner))
					continue
				}
				info, err := os.Stat(filepath.Join(root, filepath.FromSlash(clean)))
				if err != nil || info.IsDir() {
					problems = append(problems, fmt.Sprintf(missingOwnerFileFormat, c.ID, c.Phase, owner))
					continue
				}
				key := c.Phase + "\x00" + clean
				if first, ok := seen[key]; ok {
					problems = append(problems, fmt.Sprintf(overlapOwnerFileFormat, c.Phase, clean, first, c.ID))
					continue
				}
				seen[key] = c.ID
			}
		}
	}
	return problems
}

// TestBatchPackGateRejectsInvalidOwnerFiles проверяет гейт на синтетических
// пакетах. Валидный пакет проходит; отсутствующий файл, каталог вместо файла,
// выход за пределы дерева и пересечение внутри фазы отклоняются. Один и тот же
// файл в разных фазах разрешён: волны идут последовательно.
func TestBatchPackGateRejectsInvalidOwnerFiles(t *testing.T) {
	setup := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		for _, rel := range []string{"internal/a.go", "internal/b.go"} {
			path := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("package internal\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}

	for _, tc := range []struct {
		name  string
		cards []card
		want  []string
	}{
		{
			name: "valid pack passes",
			cards: []card{
				{ID: "A", Phase: "P0", Type: "task", Status: "todo", OwnerFiles: []string{"internal/a.go"}},
				{ID: "B", Phase: "P0", Type: "task", Status: "todo", OwnerFiles: []string{"internal/b.go"}},
			},
		},
		{
			name: "missing file rejected",
			cards: []card{
				{ID: "A", Phase: "P0", OwnerFiles: []string{"internal/a.go", "internal/gone.go"}},
			},
			want: []string{fmt.Sprintf(missingOwnerFileFormat, "A", "P0", "internal/gone.go")},
		},
		{
			name: "directory is not an owner file",
			cards: []card{
				{ID: "A", Phase: "P0", OwnerFiles: []string{"internal"}},
			},
			want: []string{fmt.Sprintf(missingOwnerFileFormat, "A", "P0", "internal")},
		},
		{
			name: "path outside tree rejected",
			cards: []card{
				{ID: "A", Phase: "P0", OwnerFiles: []string{"../../etc/passwd"}},
			},
			want: []string{fmt.Sprintf(missingOwnerFileFormat, "A", "P0", "../../etc/passwd")},
		},
		{
			name: "overlap inside one phase rejected",
			cards: []card{
				{ID: "A", Phase: "P0", OwnerFiles: []string{"internal/a.go"}},
				{ID: "B", Phase: "P0", OwnerFiles: []string{"internal/a.go"}},
			},
			want: []string{fmt.Sprintf(overlapOwnerFileFormat, "P0", "internal/a.go", "A", "B")},
		},
		{
			name: "same file in different phases allowed",
			cards: []card{
				{ID: "A", Phase: "P0", OwnerFiles: []string{"internal/a.go"}},
				{ID: "B", Phase: "P1", OwnerFiles: []string{"internal/a.go"}},
			},
		},
		{
			name: "generated docs stay exempt",
			cards: []card{
				{ID: "A", Phase: "P0", OwnerFiles: []string{"docs/FACTS.md"}},
				{ID: "B", Phase: "P0", OwnerFiles: []string{"docs/adrs/README.md"}},
			},
		},
		{
			name: "existing field validation still runs",
			cards: []card{
				{ID: "A", Phase: "P9", Type: "spike", Status: "closed", OwnerFiles: []string{"internal/a.go"}},
			},
			want: []string{
				"недопустимое значение поля type: spike\n",
				"недопустимое значение поля phase: P9\n",
				"недопустимое значение поля status: closed\n",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := setup(t)
			got := batchPackProblems(root, []batch{{Cards: tc.cards}})
			if len(got) != len(tc.want) {
				t.Fatalf("problems = %q, want %q", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("problem %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestBatchPackGateReadsEveryJSON proves the gate reads every research JSON in
// a directory instead of one hard-coded file. A Markdown file nearby is not a
// pack and must be ignored. Both files reach the same validator, so an invalid
// pack sitting next to a valid one still fails the gate.
func TestBatchPackGateReadsEveryJSON(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "docs", "research")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash("internal/a.go"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package internal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"01-good-batch.json": `{"cards":[{"id":"A","phase":"P0","type":"task","status":"todo","owner_files":["internal/a.go"]}]}`,
		"02-bad-batch.json":  `{"cards":[{"id":"B","phase":"P0","type":"task","status":"todo","owner_files":["internal/a.go","internal/gone.go"]}]}`,
		"notes.md":           "not a batch",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	batches, loadProblems := loadBatches(dir)
	if len(loadProblems) != 0 {
		t.Fatalf("loadProblems = %q, want none", loadProblems)
	}
	if len(batches) != 2 {
		t.Fatalf("loaded %d batches, want 2", len(batches))
	}
	got := batchPackProblems(root, batches)
	want := []string{
		fmt.Sprintf(overlapOwnerFileFormat, "P0", "internal/a.go", "A", "B"),
		fmt.Sprintf(missingOwnerFileFormat, "B", "P0", "internal/gone.go"),
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

// fakeClock returns a clock that advances by step on every read. Tests use it
// to pin the held duration in the chunk report instead of measuring real time.
func fakeClock(step time.Duration) func() time.Time {
	base := time.Unix(0, 0)
	n := 0
	return func() time.Time {
		t := base.Add(step * time.Duration(n))
		n++
		return t
	}
}

// chunkFixtures builds n distinct card artifacts for chunking tests.
func chunkFixtures(n int) []artifactEntry {
	entries := make([]artifactEntry, n)
	for i := range entries {
		entries[i] = artifactEntry{
			path:    fmt.Sprintf("docs/tasks/CBX-T-%04d.md", i),
			content: "# тест\n",
		}
	}
	return entries
}

// TestChunkEntriesPreservesOrderAndCount proves a 1000 record import at chunk
// size 50 yields 20 ordered chunks of at most 50 records. This is the
// measurable check from CBX-06: no commit is separated by more than 50 writes.
func TestChunkEntriesPreservesOrderAndCount(t *testing.T) {
	entries := chunkFixtures(1000)
	chunks := chunkEntries(entries, 50)
	if len(chunks) != 20 {
		t.Fatalf("chunks = %d, want 20", len(chunks))
	}
	seen := 0
	for _, c := range chunks {
		if len(c) > 50 {
			t.Fatalf("chunk holds %d records, want at most 50", len(c))
		}
		for _, e := range c {
			if want := entries[seen]; e != want {
				t.Fatalf("record %d = %+v, want %+v", seen, e, want)
			}
			seen++
		}
	}
	if seen != len(entries) {
		t.Fatalf("emitted %d records, want %d", seen, len(entries))
	}
}

// TestChunkedWriteCommitsAndProgress proves the writer commits once per chunk,
// prints one progress line per chunk plus a summary, and pauses only between
// chunks. The commit counts are the real batch counts, not placeholders.
func TestChunkedWriteCommitsAndProgress(t *testing.T) {
	dir := t.TempDir()
	entries := chunkFixtures(50)
	var progress strings.Builder
	var pauses []time.Duration
	commits := 0
	r := &runner{
		opts:     Options{ChunkSize: 10, ChunkPause: 2 * time.Millisecond, Commit: true, Quiet: true},
		out:      io.Discard,
		progress: &progress,
		now:      fakeClock(time.Millisecond),
		yield:    func(d time.Duration) { pauses = append(pauses, d) },
		commit: func(root string, cards, adrs, evals int) error {
			commits++
			if root != dir {
				t.Errorf("commit root = %q, want %q", root, dir)
			}
			if cards != 7 || adrs != 3 || evals != 2 {
				t.Errorf("commit counts = %d/%d/%d, want 7/3/2", cards, adrs, evals)
			}
			return nil
		},
	}
	if err := r.writeChunked(dir, entries, 7, 3, 2); err != nil {
		t.Fatal(err)
	}
	if commits != 5 {
		t.Fatalf("commits = %d, want 5", commits)
	}
	lines := strings.Split(strings.TrimSuffix(progress.String(), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("progress lines = %d, want 6: %q", len(lines), progress.String())
	}
	for i := 0; i < 5; i++ {
		want := fmt.Sprintf("chunk=%d/%d records=10 held=1ms", i+1, 5)
		if lines[i] != want {
			t.Fatalf("progress line %d = %q, want %q", i, lines[i], want)
		}
	}
	if want := "chunks=5 maxheld=1ms"; lines[5] != want {
		t.Fatalf("summary = %q, want %q", lines[5], want)
	}
	if len(pauses) != 4 {
		t.Fatalf("pauses = %d, want 4", len(pauses))
	}
	for _, d := range pauses {
		if d != 2*time.Millisecond {
			t.Fatalf("pause = %s, want 2ms", d)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, entries[0].path)); err != nil {
		t.Fatalf("first artifact missing: %v", err)
	}
}

// TestChunkPauseAllowsReader reproduces the beads finding: with a positive
// pause the writer yields between chunks and a concurrent reader advances;
// at zero pause the writer never yields and the reader is starved.
func TestChunkPauseAllowsReader(t *testing.T) {
	oldSize, oldPause := chunkSize, chunkPause
	t.Cleanup(func() { chunkSize, chunkPause = oldSize, oldPause })
	entries := chunkFixtures(1000)
	dir := t.TempDir()

	run := func(pause time.Duration) (yields, reads int, seen []time.Duration) {
		chunkSize, chunkPause = 50, pause
		var progress strings.Builder
		r := &runner{
			opts:     Options{DryRun: true, Quiet: true},
			out:      io.Discard,
			progress: &progress,
			now:      fakeClock(time.Microsecond),
			yield: func(d time.Duration) {
				yields++
				seen = append(seen, d)
				// A reader makes progress only in the window between chunks.
				if d > 0 {
					for i := 0; i < 10; i++ {
						reads++
					}
				}
			},
		}
		if err := r.writeChunked(dir, entries, 0, 0, 0); err != nil {
			t.Fatal(err)
		}
		return yields, reads, seen
	}

	yields, reads, pauses := run(time.Millisecond)
	if yields != 19 {
		t.Fatalf("paused yields = %d, want 19", yields)
	}
	if reads < 10 {
		t.Fatalf("reader turns = %d, want at least 10", reads)
	}
	for _, d := range pauses {
		if d != time.Millisecond {
			t.Fatalf("paused yield got %s, want 1ms", d)
		}
	}

	yields, reads, pauses = run(0)
	if yields != 19 {
		t.Fatalf("zero-pause yields = %d, want 19", yields)
	}
	if reads != 0 {
		t.Fatalf("zero-pause reader turns = %d, want 0", reads)
	}
	for _, d := range pauses {
		if d != 0 {
			t.Fatalf("zero-pause yield got %s, want 0", d)
		}
	}
}

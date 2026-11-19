package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/app/taskcli"
	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

func realRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func testConfig(root string) taskcli.Config {
	return taskcli.Config{
		TasksDir: filepath.Join(root, "docs", "tasks"),
		ADRDir:   filepath.Join(root, "docs", "adrs"),
		StateDir: filepath.Join(root, "state-test"),
		Root:     root,
	}
}

func TestTaskCommandListRealCards(t *testing.T) {
	root := realRoot(t)
	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "list", "--format", "json"}, &stdout, &stderr, testConfig(root))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("код %d, stderr %q", code, stderr.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("неверный JSON: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "docs", "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			want++
		}
	}
	if len(rows) != want {
		t.Fatalf("в JSON %d карточек, на диске %d", len(rows), want)
	}
	if len(rows) < 52 {
		t.Fatalf("ожидались все карточки, получено %d", len(rows))
	}
	if rows[0]["id"] != "T0.1" {
		t.Fatalf("первая карточка %v, ждали T0.1", rows[0]["id"])
	}
}

func TestTaskCommandExitCodesAndStreams(t *testing.T) {
	root := realRoot(t)
	cfg := testConfig(root)
	cases := []struct {
		name string
		argv []string
		code int
	}{
		{"нет подкоманды", []string{"task"}, 1},
		{"неизвестная подкоманда", []string{"task", "frobnicate"}, 1},
		{"неизвестный флаг", []string{"task", "list", "--bogus"}, 1},
		{"неверный формат", []string{"task", "list", "--format", "yaml"}, 1},
		{"неверный статус", []string{"task", "list", "--status", "open"}, 1},
		{"new без флагов", []string{"task", "new"}, 1},
		{"take без агента", []string{"task", "take", "T2.1"}, 1},
		{"take без ID", []string{"task", "take"}, 1},
		{"take с двумя ID", []string{"task", "take", "T2.1", "T2.2"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := TaskCommandWithConfig(tc.argv, &stdout, &stderr, cfg)
			if code != tc.code {
				t.Errorf("код = %d, ждали %d", code, tc.code)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout не пустой при ошибке: %q", stdout.String())
			}
			if stderr.Len() == 0 {
				t.Errorf("пустой stderr при ошибке")
			}
		})
	}
}

func TestTaskCommandNewDryRunTakeAndSelfTest(t *testing.T) {
	root := t.TempDir()
	tasksDir := filepath.Join(root, "docs", "tasks")
	adrDir := filepath.Join(root, "docs", "adrs")
	for _, d := range []string{tasksDir, adrDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(adrDir, "0001-accepted.md"), []byte("Статус: accepted\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := taskcli.Config{
		TasksDir: tasksDir, ADRDir: adrDir,
		StateDir: filepath.Join(root, "state"), Root: root,
	}
	newArgs := []string{"task", "new", "--phase", "P2", "--title", "Командная карточка",
		"--reference", "ADR 0001", "--owner-files", "internal/app/cmd/one.go"}
	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig(append([]string{}, append(newArgs, "--dry-run")...), &stdout, &stderr, cfg)
	if code != 0 || !strings.HasPrefix(stdout.String(), "T2.1 ") {
		t.Fatalf("dry-run: код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(tasksDir, "T2.1.md")); !os.IsNotExist(err) {
		t.Fatalf("dry-run создал файл")
	}
	stdout.Reset()
	stderr.Reset()
	code = TaskCommandWithConfig(newArgs, &stdout, &stderr, cfg)
	if code != 0 || !strings.HasPrefix(stdout.String(), "T2.1 ") {
		t.Fatalf("new: код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = TaskCommandWithConfig([]string{"task", "take", "T2.1", "--agent", "cli-agent",
		"--time", "2026-11-19T14:03:21Z"}, &stdout, &stderr, cfg)
	if code != 0 || strings.TrimSpace(stdout.String()) != "T2.1 in_progress cli-agent 2026-11-19T14:03:21Z" {
		t.Fatalf("take: код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = TaskCommandWithConfig([]string{"task", "take", "T2.1", "--agent", "other"}, &stdout, &stderr, cfg)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "cli-agent") {
		t.Fatalf("второй take: код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = TaskCommandWithConfig([]string{"task", "--self-test"}, &stdout, &stderr, cfg)
	if code != 0 || !strings.Contains(stdout.String(), "self-test passed") {
		t.Fatalf("self-test: код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
}

// writeWhyCard пишет карточку с обязательными полями для тестов why.
func writeWhyCard(t *testing.T, dir, id, status, agent, deps, context string) {
	t.Helper()
	body := fmt.Sprintf(`# %s. Карточка %s

Тип: task
Статус: %s
Фаза: P1
Владелец файлов: cmd/%s.go
Зависимости: %s
Ссылка: ADR 0001
Цель: Проверить цепочку
Контекст: %s
Шаги: Шаг
Проверка: Проверка
Приёмка: Приёмка
Агент: %s
Время: 2026-11-20T00:00:00Z
`, id, id, status, strings.ReplaceAll(id, ".", "-"), deps, context, agent)
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func whyConfig(dir string) taskcli.Config {
	return taskcli.Config{TasksDir: dir, ADRDir: dir, StateDir: dir, Root: dir}
}

func TestTaskCommandWhyChain(t *testing.T) {
	dir := t.TempDir()
	writeWhyCard(t, dir, "T1.1", "todo", "", "T1.2", "цепочка")
	writeWhyCard(t, dir, "T1.2", "in_progress", "alice", "T1.3", "цепочка")
	writeWhyCard(t, dir, "T1.3", "todo", "", "нет", "цепочка")

	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "why", "T1.1"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("код %d, stderr %q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"T1.1", "T1.2", "depends-on", "alice", "in_progress", "T1.3", "todo"} {
		if !strings.Contains(out, want) {
			t.Errorf("в выводе нет %q: %q", want, out)
		}
	}
	if !strings.Contains(out, "1. T1.2 через depends-on") || !strings.Contains(out, "2. T1.3 через depends-on") {
		t.Errorf("неверный порядок шагов: %q", out)
	}

	// Команда принимает и форму без префикса task.
	stdout.Reset()
	stderr.Reset()
	code = TaskCommandWithConfig([]string{"why", "T1.1"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || !strings.Contains(stdout.String(), "T1.2 через depends-on") {
		t.Fatalf("alias why: код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestTaskCommandWhyEdgeKinds(t *testing.T) {
	dir := t.TempDir()
	edges := []struct{ from, to, kind string }{
		{"T2.1", "T2.2", "depends-on"},
		{"T2.2", "T2.3", "conflicts-with"},
		{"T2.3", "T2.4", "part-of"},
		{"T2.4", "T2.5", "derived-from"},
		{"T2.5", "T2.6", "supersedes"},
		{"T2.6", "T2.7", "same-as"},
	}
	for i, e := range edges {
		deps := "нет"
		status := "in_progress"
		agent := "holder-" + e.from
		if i == 0 {
			status = "todo"
		}
		deps = e.kind + ":" + e.to
		writeWhyCard(t, dir, e.from, status, agent, deps, "цепочка")
	}
	writeWhyCard(t, dir, "T2.7", "in_progress", "holder-T2.7", "нет", "цепочка")

	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "why", "T2.1"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("код %d, stderr %q", code, stderr.String())
	}
	out := stdout.String()
	for _, kind := range whyEdgeKinds {
		if !strings.Contains(out, "через "+kind+":") {
			t.Errorf("в выводе нет ребра %q: %q", kind, out)
		}
	}
	for _, e := range edges {
		if !strings.Contains(out, "holder-"+e.to) {
			t.Errorf("в выводе нет держателя %q: %q", e.to, out)
		}
	}
}

func TestTaskCommandWhyIgnoresBlockedText(t *testing.T) {
	dir := t.TempDir()
	writeWhyCard(t, dir, "T3.1", "todo", "", "нет", "карточка blocked без цепочки")

	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "why", "T3.1"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("код %d, stderr %q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "цепочка блокировок не найдена") {
		t.Errorf("нет сообщения о пустой цепочке: %q", out)
	}
	if strings.Contains(out, "blocked") {
		t.Errorf("вывод построен по тексту статуса: %q", out)
	}
}

func TestTaskCommandWhyValidation(t *testing.T) {
	dir := t.TempDir()
	writeWhyCard(t, dir, "T4.1", "todo", "", "нет", "цепочка")
	cfg := whyConfig(dir)
	cases := []struct {
		name string
		argv []string
	}{
		{"без ID", []string{"task", "why"}},
		{"два ID", []string{"task", "why", "T4.1", "T4.2"}},
		{"неверный ID", []string{"task", "why", "four.one"}},
		{"неизвестная карточка", []string{"task", "why", "T4.9"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := TaskCommandWithConfig(tc.argv, &stdout, &stderr, cfg)
			if code != 1 {
				t.Errorf("код = %d, ждали 1", code)
			}
			if stdout.Len() != 0 || stderr.Len() == 0 {
				t.Errorf("stdout %q, stderr %q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestFindProjectRoot(t *testing.T) {
	root := findProjectRoot()
	if root == "" {
		t.Fatal("пустой корень проекта")
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("go.mod не найден в %s: %v", root, err)
	}
}

// ---- ADR 0051 п4 / T3.120 ----

func writeCompletionCard(t *testing.T, dir, id, status string) string {
	t.Helper()
	body := fmt.Sprintf(`# %s. Карточка завершения
Тип: task
Статус: %s
Фаза: P1
Владелец файлов: cmd/beacon/task.go
Зависимости: нет
Ссылка: ADR 0001
Агент: тест
Время: 2026-11-20T00:00:00Z
Цель: Проверить завершение
Контекст: ADR 0051 п4
Шаги: Шаг
Проверка: Проверка
Приёмка: Приёмка
`, id, status)
	path := filepath.Join(dir, id+".md")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTaskCommandDoneRejectsMissingEvidence(t *testing.T) {
	cases := []struct {
		name   string
		argv   []string
		want   string
		parked bool
	}{
		{"без полей", []string{"task", "done", "T5.1"}, "доказательства", true},
		{"только итог", []string{"task", "done", "T5.1", "--summary", "готово"}, "доказательства", true},
		{"пустое доказательство", []string{"task", "done", "T5.1", "--summary", "готово", "--evidence", "   "}, "доказательства", true},
		{"только доказательство", []string{"task", "done", "T5.1", "--evidence", "go test"}, "итога", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeCompletionCard(t, dir, "T5.1", "in_progress")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := TaskCommandWithConfig(tc.argv, &stdout, &stderr, whyConfig(dir))
			if code != 1 {
				t.Errorf("код = %d, ждали 1", code)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout не пустой при отказе: %q", stdout.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("в stderr нет %q: %q", tc.want, stderr.String())
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.parked {
				if string(after) != string(before) {
					t.Errorf("карточка изменена при отказе: %q", after)
				}
				return
			}
			// Отказ без доказательства не закрывает задачу и оставляет её в needs_input.
			body := string(after)
			if strings.Contains(body, "Статус: done") {
				t.Errorf("задача закрыта без доказательства: %q", body)
			}
			for _, want := range []string{"Статус: in_progress", "Состояние: needs_input", "--evidence"} {
				if !strings.Contains(body, want) {
					t.Errorf("в карточке нет %q: %q", want, body)
				}
			}
			cards, err := taskstore.LoadCards(dir)
			if err != nil {
				t.Fatalf("карточка в needs_input не разбирается: %v", err)
			}
			if len(cards) != 1 || cards[0].Status != "in_progress" {
				t.Fatalf("карточки после отказа: %+v", cards)
			}
		})
	}
}

func TestTaskCommandDoneWithEvidence(t *testing.T) {
	dir := t.TempDir()
	path := writeCompletionCard(t, dir, "T5.2", "in_progress")
	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "done", "T5.2",
		"--summary", "фича готова", "--evidence", "go test ./cmd/beacon\n"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("код %d, stderr %q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "T5.2 done" {
		t.Fatalf("stdout %q", stdout.String())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Статус: done", "Итог: фича готова", "Доказательство: go test ./cmd/beacon"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("в карточке нет %q: %q", want, body)
		}
	}
	// Карточка с новыми полями обязана разбираться основным парсером.
	cards, err := taskstore.LoadCards(dir)
	if err != nil {
		t.Fatalf("карточка после done не разбирается: %v", err)
	}
	if len(cards) != 1 || cards[0].Status != "done" {
		t.Fatalf("карточки после done: %+v", cards)
	}
	// Повторное завершение готовой карточки отклоняется.
	stdout.Reset()
	stderr.Reset()
	code = TaskCommandWithConfig([]string{"task", "done", "T5.2",
		"--summary", "ещё раз", "--evidence", "go test"}, &stdout, &stderr, whyConfig(dir))
	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("повторный done: код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestTaskCommandNeedsInput(t *testing.T) {
	dir := t.TempDir()
	path := writeCompletionCard(t, dir, "T5.3", "todo")
	var stdout, stderr bytes.Buffer

	// Завершение без доказательства не закрывает карточку и ставит needs_input.
	parkPath := writeCompletionCard(t, dir, "T5.5", "in_progress")
	stdout.Reset()
	stderr.Reset()
	code := TaskCommandWithConfig([]string{"task", "done", "T5.5", "--summary", "готово"},
		&stdout, &stderr, whyConfig(dir))
	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("done без доказательства: код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	parked, err := os.ReadFile(parkPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(parked), "Статус: done") ||
		!strings.Contains(string(parked), "Состояние: needs_input") {
		t.Fatalf("карточка без доказательства не переведена в needs_input: %q", parked)
	}
	stdout.Reset()
	stderr.Reset()

	code = TaskCommandWithConfig([]string{"task", "needs-input", "T5.3",
		"--question", "нужен доступ к PR?"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("needs-input: код %d stderr %q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "T5.3 needs_input" {
		t.Fatalf("stdout %q", stdout.String())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Статус: in_progress", "Состояние: needs_input", "Вопрос: нужен доступ к PR?"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("в карточке нет %q: %q", want, body)
		}
	}
	cards, err := taskstore.LoadCards(dir)
	if err != nil {
		t.Fatalf("карточка с needs_input не разбирается: %v", err)
	}
	found := false
	for _, c := range cards {
		if c.ID == "T5.3" {
			found = true
			if c.Status != "in_progress" {
				t.Fatalf("статус T5.3 = %q, ждали in_progress", c.Status)
			}
		}
	}
	if !found {
		t.Fatalf("карточка T5.3 не найдена: %+v", cards)
	}

	// Завершение после ответа снимает маркер needs_input и требует доказательство.
	stdout.Reset()
	stderr.Reset()
	code = TaskCommandWithConfig([]string{"task", "done", "T5.3",
		"--summary", "ответ получен", "--evidence", "gh issue view 1 --comments"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("done после needs_input: код %d stderr %q", code, stderr.String())
	}
	body, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Статус: done") {
		t.Errorf("нет статуса done: %q", body)
	}
	if strings.Contains(string(body), "needs_input") || strings.Contains(string(body), "Вопрос:") {
		t.Errorf("маркер needs_input не снят: %q", body)
	}
}

func TestTaskCommandNeedsInputRequiresQuestion(t *testing.T) {
	dir := t.TempDir()
	path := writeCompletionCard(t, dir, "T5.4", "in_progress")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "needs-input", "T5.4"}, &stdout, &stderr, whyConfig(dir))
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "--question") {
		t.Fatalf("код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("карточка изменена при отказе: %q", after)
	}
}

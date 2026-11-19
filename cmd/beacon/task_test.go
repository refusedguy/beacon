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
		agent := "holder-" + e.to
		if i == 0 {
			status, agent = "todo", ""
		}
		if i < len(edges)-1 {
			deps = edges[i+1].kind + ":" + edges[i+1].to
		}
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

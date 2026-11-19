package main

import (
	"bytes"
	"encoding/json"
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

func TestFindProjectRoot(t *testing.T) {
	root := findProjectRoot()
	if root == "" {
		t.Fatal("пустой корень проекта")
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("go.mod не найден в %s: %v", root, err)
	}
}

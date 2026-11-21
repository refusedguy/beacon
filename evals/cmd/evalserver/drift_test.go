//go:build eval

package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestScanRepoEmptyCardsFailsClosed(t *testing.T) {
	findings := scanRepo(t.TempDir())
	if !hasFinding(findings, noCardsMsg) {
		t.Fatalf("пустой набор карточек должен давать FAIL, получили: %+v", findings)
	}
}

func TestScanRepoNoActivePhaseFailsClosed(t *testing.T) {
	root := t.TempDir()
	card := "# HD-997. Карточка без фазы\nСтатус: new\n"
	if err := writeFixture(root, "docs/tasks/HD-997.md", card); err != nil {
		t.Fatalf("записать карточку без фазы: %v", err)
	}
	findings := scanRepo(root)
	if !hasFinding(findings, noPhaseMsg) {
		t.Fatalf("план без активной фазы должен давать FAIL, получили: %+v", findings)
	}
}

func TestScanRepoPositivePathStaysClean(t *testing.T) {
	root := t.TempDir()
	card := "# HD-998. Актуальный план\nСтатус: done\nФаза: P8\n" +
		"Владелец файлов: internal/clean/hd998.go\n\n" +
		"## Подтверждение\n- PR #1 MERGED: 2026-11-20 (squash).\n"
	if err := writeFixture(root, "docs/tasks/HD-998.md", card); err != nil {
		t.Fatalf("записать карточку: %v", err)
	}
	if err := writeFixture(root, "internal/clean/hd998.go", "package clean\n"); err != nil {
		t.Fatalf("записать файл владельца: %v", err)
	}
	if findings := scanRepo(root); len(findings) != 0 {
		t.Fatalf("чистый путь дал ложные находки: %+v", findings)
	}
}

func TestRunDriftCheckEmptyRepoFailsWithReason(t *testing.T) {
	t.Setenv("BEACON_REPO_ROOT", t.TempDir())
	var code int
	out := captureDriftStdout(t, func() { code = runDriftCheck() })
	if code != 1 {
		t.Fatalf("runDriftCheck() = %d, want 1", code)
	}
	if !strings.Contains(out, noCardsMsg) {
		t.Fatalf("вывод не содержит причину %q:\n%s", noCardsMsg, out)
	}
	if !strings.Contains(out, "Результат: FAIL") {
		t.Fatalf("вывод не содержит FAIL:\n%s", out)
	}
}

func TestRunDriftCheckCleanRepoPasses(t *testing.T) {
	root := t.TempDir()
	card := "# HD-998. Актуальный план\nСтатус: done\nФаза: P8\n" +
		"Владелец файлов: internal/clean/hd998.go\n\n" +
		"## Подтверждение\n- PR #1 MERGED: 2026-11-20 (squash).\n"
	if err := writeFixture(root, "docs/tasks/HD-998.md", card); err != nil {
		t.Fatalf("записать карточку: %v", err)
	}
	if err := writeFixture(root, "internal/clean/hd998.go", "package clean\n"); err != nil {
		t.Fatalf("записать файл владельца: %v", err)
	}
	t.Setenv("BEACON_REPO_ROOT", root)
	var code int
	captureDriftStdout(t, func() { code = runDriftCheck() })
	if code != 0 {
		t.Fatalf("runDriftCheck() = %d, want 0", code)
	}
}

func captureDriftStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("создать pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = original }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("закрыть pipe: %v", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("прочитать pipe: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("закрыть pipe: %v", err)
	}
	return string(data)
}

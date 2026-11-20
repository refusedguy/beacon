package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateDirLegacyOutsideProject(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	t.Setenv(beaconStateDirEnv, "")
	cwd := t.TempDir()
	t.Chdir(cwd)

	got, err := stateDir()
	if err != nil {
		t.Fatalf("stateDir: %v", err)
	}
	want := filepath.Join(base, "beacon")
	if got != want {
		t.Fatalf("stateDir = %q, want legacy %q", got, want)
	}
}

func TestStateDirScopedToProject(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	t.Setenv(beaconStateDirEnv, "")
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("mk .git: %v", err)
	}
	t.Chdir(project)

	first, err := stateDir()
	if err != nil {
		t.Fatalf("stateDir: %v", err)
	}
	second, err := stateDir()
	if err != nil {
		t.Fatalf("stateDir again: %v", err)
	}
	if first != second {
		t.Fatalf("stateDir is not stable: %q != %q", first, second)
	}
	if !strings.Contains(first, filepath.Join("beacon", "projects")+string(filepath.Separator)) {
		t.Fatalf("stateDir = %q, want a project-scoped path", first)
	}
	if !strings.HasPrefix(filepath.Base(first), filepath.Base(project)+"-") {
		t.Fatalf("stateDir base %q, want project name prefix %q", filepath.Base(first), filepath.Base(project))
	}

	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, ".git"), []byte("gitdir: somewhere\n"), 0o644); err != nil {
		t.Fatalf("write worktree .git: %v", err)
	}
	t.Chdir(other)
	gotOther, err := stateDir()
	if err != nil {
		t.Fatalf("stateDir other: %v", err)
	}
	if gotOther == first {
		t.Fatalf("two projects share state dir %q", first)
	}
}

func TestStateDirExplicitOverride(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	override := filepath.Join(t.TempDir(), "custom-state")
	t.Setenv(beaconStateDirEnv, override)

	got, err := stateDir()
	if err != nil {
		t.Fatalf("stateDir: %v", err)
	}
	if got != override {
		t.Fatalf("stateDir = %q, want override %q", got, override)
	}
}

func TestProjectStateKeyStableAndDistinct(t *testing.T) {
	a := filepath.Join(t.TempDir(), "same-name")
	b := filepath.Join(t.TempDir(), "same-name")
	for _, dir := range []string{a, b} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	keyA, err := projectStateKey(a)
	if err != nil {
		t.Fatalf("projectStateKey a: %v", err)
	}
	keyA2, err := projectStateKey(a)
	if err != nil {
		t.Fatalf("projectStateKey a again: %v", err)
	}
	keyB, err := projectStateKey(b)
	if err != nil {
		t.Fatalf("projectStateKey b: %v", err)
	}
	if keyA != keyA2 {
		t.Fatalf("key is not stable: %q != %q", keyA, keyA2)
	}
	if keyA == keyB {
		t.Fatalf("different absolute paths share key %q", keyA)
	}
}

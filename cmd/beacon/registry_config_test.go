package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeRegistryProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mk .git: %v", err)
	}
	return root
}

func writeRegistryConfig(t *testing.T, root, body string) string {
	t.Helper()
	dir := filepath.Join(root, registryConfigDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mk %s: %v", dir, err)
	}
	path := filepath.Join(dir, registryConfigFile)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestFindRegistryConfigFromSubdirectory(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	t.Setenv(beaconStateDirEnv, "")
	t.Setenv(beaconProjectRootEnv, "")

	root := makeRegistryProject(t)
	wantPath := writeRegistryConfig(t, root, "schema: beacon.agent-registry/v1\n")
	nested := filepath.Join(root, "docs", "tasks")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mk nested: %v", err)
	}
	t.Chdir(nested)

	got, err := findRegistryConfig("")
	if err != nil {
		t.Fatalf("findRegistryConfig: %v", err)
	}
	if got.Global {
		t.Fatal("config inside git must not be global")
	}
	if got.Path != wantPath {
		t.Fatalf("Path = %q, want %q", got.Path, wantPath)
	}
	if got.Root != root {
		t.Fatalf("Root = %q, want %q", got.Root, root)
	}
	key, err := projectStateKey(root)
	if err != nil {
		t.Fatalf("projectStateKey: %v", err)
	}
	if got.Key != key {
		t.Fatalf("Key = %q, want %q", got.Key, key)
	}
}

func TestFindRegistryConfigNearestConfigWins(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	t.Setenv(beaconStateDirEnv, "")
	t.Setenv(beaconProjectRootEnv, "")

	root := makeRegistryProject(t)
	writeRegistryConfig(t, root, "schema: root\n")
	nested := filepath.Join(root, "sub")
	wantPath := writeRegistryConfig(t, nested, "schema: nested\n")
	deep := filepath.Join(nested, "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mk deep: %v", err)
	}
	t.Chdir(deep)

	got, err := findRegistryConfig("")
	if err != nil {
		t.Fatalf("findRegistryConfig: %v", err)
	}
	if got.Path != wantPath {
		t.Fatalf("Path = %q, want nearest %q", got.Path, wantPath)
	}
	key, err := projectStateKey(nested)
	if err != nil {
		t.Fatalf("projectStateKey: %v", err)
	}
	if got.Key != key {
		t.Fatalf("Key = %q, want nested key %q", got.Key, key)
	}
}

func TestFindRegistryConfigExplicitProjectDistinctKeys(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	t.Setenv(beaconStateDirEnv, "")
	t.Setenv(beaconProjectRootEnv, "")
	t.Chdir(t.TempDir())

	a := filepath.Join(t.TempDir(), "same-name")
	b := filepath.Join(t.TempDir(), "same-name")
	for _, dir := range []string{a, b} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	gotA, err := findRegistryConfig(a)
	if err != nil {
		t.Fatalf("findRegistryConfig a: %v", err)
	}
	gotB, err := findRegistryConfig(b)
	if err != nil {
		t.Fatalf("findRegistryConfig b: %v", err)
	}
	if gotA.Global || gotB.Global {
		t.Fatal("explicit project must not select the global config")
	}
	if gotA.Key == "" || gotB.Key == "" {
		t.Fatal("explicit project must have a key")
	}
	if gotA.Key == gotB.Key {
		t.Fatalf("two projects named same-name share key %q", gotA.Key)
	}
	if !strings.HasPrefix(gotA.Key, "same-name-") || !strings.HasPrefix(gotB.Key, "same-name-") {
		t.Fatalf("keys %q, %q must start with the folder name", gotA.Key, gotB.Key)
	}
	wantA := filepath.Join(a, registryConfigDir, registryConfigFile)
	if gotA.Path != wantA {
		t.Fatalf("Path a = %q, want %q", gotA.Path, wantA)
	}
	wantB := filepath.Join(b, registryConfigDir, registryConfigFile)
	if gotB.Path != wantB {
		t.Fatalf("Path b = %q, want %q", gotB.Path, wantB)
	}
}

func TestFindRegistryConfigMissingAtGitRoot(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	t.Setenv(beaconStateDirEnv, "")
	t.Setenv(beaconProjectRootEnv, "")

	root := makeRegistryProject(t)
	nested := filepath.Join(root, "src")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mk nested: %v", err)
	}
	t.Chdir(nested)

	got, err := findRegistryConfig("")
	if err != nil {
		t.Fatalf("findRegistryConfig: %v", err)
	}
	if got.Global {
		t.Fatal("project without config must still be project-scoped")
	}
	wantPath := filepath.Join(root, registryConfigDir, registryConfigFile)
	if got.Path != wantPath {
		t.Fatalf("Path = %q, want %q", got.Path, wantPath)
	}
}

func TestFindRegistryConfigOutsideGitUsesGlobalConfig(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	t.Setenv(beaconStateDirEnv, "")
	t.Setenv(beaconProjectRootEnv, "")
	t.Chdir(t.TempDir())

	got, err := findRegistryConfig("")
	if err != nil {
		t.Fatalf("findRegistryConfig: %v", err)
	}
	if !got.Global {
		t.Fatal("outside git must use the global config")
	}
	want := filepath.Join(base, "beacon", registryConfigFile)
	if got.Path != want {
		t.Fatalf("Path = %q, want global %q", got.Path, want)
	}
	if got.Key != "" || got.Root != "" {
		t.Fatalf("global config must have no project identity, got %+v", got)
	}
}

func TestFindRegistryConfigGlobalRespectsStateDirOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom-state")
	t.Setenv(beaconStateDirEnv, override)
	t.Setenv(beaconProjectRootEnv, "")
	t.Chdir(t.TempDir())

	got, err := findRegistryConfig("")
	if err != nil {
		t.Fatalf("findRegistryConfig: %v", err)
	}
	if !got.Global {
		t.Fatal("outside git must use the global config")
	}
	want := filepath.Join(override, registryConfigFile)
	if got.Path != want {
		t.Fatalf("Path = %q, want override %q", got.Path, want)
	}
}

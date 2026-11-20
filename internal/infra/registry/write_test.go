package registry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func configPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "agents.yaml")
}

func readConfig(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func configModeOnDisk(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func assertNoStagingFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), tempPrefix) {
			t.Fatalf("staging file left behind: %s", entry.Name())
		}
	}
}

func TestAtomicWriteConfigCreatesFile(t *testing.T) {
	path := configPath(t)
	data := []byte("schema: beacon.agent-registry\n")

	if err := AtomicWriteConfig(path, data, 0o600); err != nil {
		t.Fatalf("write new file: %v", err)
	}
	if got := readConfig(t, path); string(got) != string(data) {
		t.Fatalf("content = %q, want %q", got, data)
	}
	if got := configModeOnDisk(t, path); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	assertNoStagingFiles(t, filepath.Dir(path))
}

func TestAtomicWriteConfigPreservesExistingMode(t *testing.T) {
	path := configPath(t)
	if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := AtomicWriteConfig(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("replace file: %v", err)
	}
	if got := readConfig(t, path); string(got) != "new" {
		t.Fatalf("content = %q, want new", got)
	}
	if got := configModeOnDisk(t, path); got != 0o640 {
		t.Fatalf("mode = %o, want 640 preserved", got)
	}
	assertNoStagingFiles(t, filepath.Dir(path))
}

func TestAtomicWriteConfigRepeatedWriteSameBytes(t *testing.T) {
	path := configPath(t)
	data := []byte("{\"roster\":[],\"version\":1}\n")

	for i := 0; i < 2; i++ {
		if err := AtomicWriteConfig(path, data, 0o644); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if got := readConfig(t, path); string(got) != string(data) {
		t.Fatalf("repeated write changed bytes: got %q, want %q", got, data)
	}
	assertNoStagingFiles(t, filepath.Dir(path))
}

func TestAtomicWriteConfigZeroPermUsesDefault(t *testing.T) {
	path := configPath(t)
	if err := AtomicWriteConfig(path, []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if got := configModeOnDisk(t, path); got != defaultConfigPerm {
		t.Fatalf("mode = %o, want default %o", got, defaultConfigPerm)
	}
}

func TestAtomicWriteConfigInterruptedKeepsOriginal(t *testing.T) {
	path := configPath(t)
	original := []byte("original config\n")
	if err := os.WriteFile(path, original, 0o640); err != nil {
		t.Fatal(err)
	}

	err := writeConfig(path, []byte("replacement\n"), 0o600, func() error {
		return errors.New("interrupted")
	})
	if err == nil {
		t.Fatal("interrupted write returned nil error")
	}
	if got := readConfig(t, path); string(got) != string(original) {
		t.Fatalf("original changed after interruption: got %q, want %q", got, original)
	}
	if got := configModeOnDisk(t, path); got != 0o640 {
		t.Fatalf("original mode changed after interruption: got %o, want 640", got)
	}
	assertNoStagingFiles(t, filepath.Dir(path))
}

func TestAtomicWriteConfigMissingDirectoryFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "agents.yaml")
	if err := AtomicWriteConfig(path, []byte("x"), 0o644); err == nil {
		t.Fatal("write into a missing directory returned nil error")
	}
}

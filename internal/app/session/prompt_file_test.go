package session

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// memPromptFS is an in-memory PromptFileFS. It records the mode of each file
// and can fail creates and reads on demand so the fallback and cleanup paths
// are exercised without touching the disk.
type memPromptFS struct {
	data      map[string][]byte
	modes     map[string]os.FileMode
	failWrite bool
	readErr   map[string]error
}

func newMemPromptFS() *memPromptFS {
	return &memPromptFS{data: make(map[string][]byte), modes: make(map[string]os.FileMode), readErr: make(map[string]error)}
}

func (m *memPromptFS) Create(dir, name string, data []byte) (string, error) {
	if m.failWrite {
		return "", errors.New("disk full")
	}
	path := filepath.Join(dir, name)
	if _, ok := m.data[path]; ok {
		return "", os.ErrExist
	}
	m.data[path] = append([]byte(nil), data...)
	m.modes[path] = PromptFilePerm
	return path, nil
}

func (m *memPromptFS) Read(path string) ([]byte, error) {
	if err := m.readErr[path]; err != nil {
		return nil, err
	}
	data, ok := m.data[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (m *memPromptFS) Remove(path string) error {
	if _, ok := m.data[path]; !ok {
		return os.ErrNotExist
	}
	delete(m.data, path)
	delete(m.modes, path)
	return nil
}

func (m *memPromptFS) Names(dir string) ([]string, error) {
	prefix := dir + string(os.PathSeparator)
	names := make([]string, 0, len(m.data))
	for path := range m.data {
		if strings.HasPrefix(path, prefix) {
			names = append(names, strings.TrimPrefix(path, prefix))
		}
	}
	sort.Strings(names)
	return names, nil
}

func fixedStore(fs PromptFileFS, dir string) *PromptFileStore {
	s := NewPromptFileStore(fs, dir)
	s.now = func() time.Time { return time.Date(2026, 11, 22, 10, 30, 0, 0, time.UTC) }
	s.suffix = func() (string, error) { return "ab12cd34ef56", nil }
	return s
}

func TestPromptFileWriteReadByteExact(t *testing.T) {
	prompt := "line 'single' \"double\" `backtick` $HOME\nsecond\tline"
	fs := newMemPromptFS()
	s := fixedStore(fs, "/state/prompts")

	path, err := s.Write(prompt, "CD-14")
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := fs.modes[path]; got != PromptFilePerm {
		t.Errorf("mode = %o, want %o", got, PromptFilePerm)
	}
	if !strings.Contains(path, "cd-14-") || !strings.HasSuffix(path, ".prompt") {
		t.Errorf("path = %q, want slug prefix and .prompt suffix", path)
	}

	got, err := s.ReadAndDelete(path)
	if err != nil {
		t.Fatalf("ReadAndDelete: %v", err)
	}
	if !bytes.Equal(got, []byte(prompt)) {
		t.Errorf("read back %q, want %q", got, prompt)
	}
	if len(fs.data) != 0 {
		t.Errorf("files after read = %d, want 0", len(fs.data))
	}
}

func TestPromptFileWriteWithoutFS(t *testing.T) {
	s := NewPromptFileStore(nil, "/state/prompts")
	if _, err := s.Write("x", "CD-14"); !errors.Is(err, ErrPromptFileWrite) {
		t.Errorf("Write error = %v, want ErrPromptFileWrite", err)
	}
}

func TestPromptFileReadDeleteRemovesFileWhenReadFails(t *testing.T) {
	fs := newMemPromptFS()
	s := fixedStore(fs, "/state/prompts")
	path, err := s.Write("prompt", "CD-14")
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	fs.readErr[path] = errors.New("read failed")

	if _, err := s.ReadAndDelete(path); err == nil {
		t.Fatal("ReadAndDelete succeeded, want error")
	}
	if len(fs.data) != 0 {
		t.Errorf("files after failed read = %d, want 0", len(fs.data))
	}
}

func TestPromptFileReadDeleteCommandKeepsPromptOutOfCommand(t *testing.T) {
	prompt := "secret `$(whoami)` 'value'"
	s := fixedStore(newMemPromptFS(), "/state/prompts")
	path := filepath.Join("/state/prompts", "cd-14-20261122.prompt")

	for _, shell := range []string{PromptFileShellPOSIX, PromptFileShellFish} {
		cmd := s.ReadDeleteCommand(path, shell)
		if strings.Contains(cmd, prompt) || strings.Contains(cmd, "whoami") {
			t.Errorf("shell %s command embeds prompt: %q", shell, cmd)
		}
		if !strings.Contains(cmd, "rm -f") {
			t.Errorf("shell %s command has no removal: %q", shell, cmd)
		}
		if !strings.Contains(cmd, shellQuote(path)) {
			t.Errorf("shell %s command does not quote path: %q", shell, cmd)
		}
	}

	fish := s.ReadDeleteCommand(path, PromptFileShellFish)
	if !strings.Contains(fish, "string collect -N") {
		t.Errorf("fish command = %q, want string collect -N", fish)
	}
	posix := s.ReadDeleteCommand(path, PromptFileShellPOSIX)
	if !strings.Contains(posix, "${__beacon_prompt%x}") {
		t.Errorf("posix command = %q, want sentinel strip", posix)
	}
}

func TestPromptFileDeliverUsesFile(t *testing.T) {
	fs := newMemPromptFS()
	s := fixedStore(fs, "/state/prompts")

	delivery := s.Deliver("prompt with ' quotes", "CD-14", PromptFileShellPOSIX)
	if !delivery.UsedFile {
		t.Fatal("Deliver did not use a file")
	}
	if delivery.Inline != "" {
		t.Errorf("Inline = %q, want empty", delivery.Inline)
	}
	if delivery.Path == "" || delivery.Command == "" {
		t.Errorf("delivery = %+v, want path and command", delivery)
	}
	if len(fs.data) != 1 {
		t.Errorf("files after Deliver = %d, want 1", len(fs.data))
	}
}

func TestPromptFileDeliverFallbackOnWriteError(t *testing.T) {
	fs := newMemPromptFS()
	fs.failWrite = true
	s := fixedStore(fs, "/state/prompts")
	prompt := "fallback 'quoted' `cmd` $VAR\nnext"

	delivery := s.Deliver(prompt, "CD-14", PromptFileShellPOSIX)
	if delivery.UsedFile {
		t.Fatal("Deliver used a file despite write failure")
	}
	if delivery.Path != "" || delivery.Command != "" {
		t.Errorf("delivery = %+v, want no path or command", delivery)
	}
	if delivery.Inline != shellQuote(prompt) {
		t.Errorf("Inline = %q, want escaped prompt %q", delivery.Inline, shellQuote(prompt))
	}
	if len(fs.data) != 0 {
		t.Errorf("files after failed write = %d, want 0", len(fs.data))
	}
}

func TestPromptFileCleanupByPrefix(t *testing.T) {
	fs := newMemPromptFS()
	s := fixedStore(fs, "/state/prompts")

	for i := 0; i < 3; i++ {
		s.suffix = func() (string, error) { return strings.Repeat("a", i+1) + "bb", nil }
		if _, err := s.Write("prompt", "CD-14"); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	s.suffix = func() (string, error) { return "other", nil }
	if _, err := s.Write("prompt", "CD-15"); err != nil {
		t.Fatalf("Write CD-15: %v", err)
	}

	removed, err := s.Cleanup("CD-14")
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if removed != 3 {
		t.Errorf("removed = %d, want 3", removed)
	}
	if len(fs.data) != 1 {
		t.Fatalf("files after cleanup = %d, want 1", len(fs.data))
	}
	for path := range fs.data {
		if !strings.Contains(path, "cd-15-") {
			t.Errorf("cleanup removed the wrong file: %q", path)
		}
	}

	again, err := s.Cleanup("CD-14")
	if err != nil {
		t.Fatalf("second Cleanup: %v", err)
	}
	if again != 0 {
		t.Errorf("second removed = %d, want 0", again)
	}
}

func TestSanitizeSlug(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"CD-14", "cd-14"},
		{"  task/Title  ", "task-title"},
		{"a_b$c", "a_b-c"},
		{"!!!", "prompt"},
		{"", "prompt"},
	}
	for _, tc := range cases {
		if got := sanitizeSlug(tc.in); got != tc.want {
			t.Errorf("sanitizeSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

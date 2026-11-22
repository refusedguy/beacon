package taskcli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeHookFS struct {
	files     map[string][]byte
	modes     map[string]os.FileMode
	writes    int
	chmods    int
	failWrite bool
	failPath  string
}

func newFakeHookFS() *fakeHookFS {
	return &fakeHookFS{
		files: make(map[string][]byte),
		modes: make(map[string]os.FileMode),
	}
}

func (f *fakeHookFS) MkdirAll(path string, perm os.FileMode) error { return nil }

func (f *fakeHookFS) Stat(path string) (os.FileInfo, error) {
	key := filepath.Clean(path)
	data, ok := f.files[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return fakeHookFileInfo{name: filepath.Base(key), size: int64(len(data)), mode: f.modes[key]}, nil
}

func (f *fakeHookFS) ReadFile(path string) ([]byte, error) {
	data, ok := f.files[filepath.Clean(path)]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (f *fakeHookFS) WriteFile(path string, data []byte, perm os.FileMode) error {
	key := filepath.Clean(path)
	if f.failWrite && (f.failPath == "" || f.failPath == key) {
		return errors.New("disk full")
	}
	f.files[key] = append([]byte(nil), data...)
	f.modes[key] = perm
	f.writes++
	return nil
}

func (f *fakeHookFS) Chmod(path string, perm os.FileMode) error {
	f.modes[filepath.Clean(path)] = perm
	f.chmods++
	return nil
}

type fakeHookFileInfo struct {
	name string
	size int64
	mode os.FileMode
}

func (i fakeHookFileInfo) Name() string       { return i.name }
func (i fakeHookFileInfo) Size() int64        { return i.size }
func (i fakeHookFileInfo) Mode() os.FileMode  { return i.mode }
func (i fakeHookFileInfo) ModTime() time.Time { return time.Time{} }
func (i fakeHookFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fakeHookFileInfo) Sys() any           { return nil }

func TestBootstrapHooksFirstCall(t *testing.T) {
	files := DefaultHookFiles()
	docs, examples := 0, 0
	for _, file := range files {
		if file.Executable {
			examples++
		} else {
			docs++
		}
	}
	if docs != 4 || examples != 5 {
		t.Fatalf("шаблонов: документов %d, примеров %d, нужно 4 и 5", docs, examples)
	}

	fs := newFakeHookFS()
	written, err := BootstrapHooks("/hooks", fs, files)
	if err != nil {
		t.Fatal(err)
	}
	if written != 9 || fs.writes != 9 {
		t.Fatalf("записано %d файлов, вызовов WriteFile %d, нужно 9", written, fs.writes)
	}
	if fs.chmods != 5 {
		t.Fatalf("вызовов Chmod %d, нужно 5", fs.chmods)
	}

	for _, file := range files {
		key := filepath.Join("/hooks", filepath.FromSlash(file.Path))
		if _, ok := fs.files[key]; !ok {
			t.Errorf("файл %s не создан", file.Path)
		}
		want := os.FileMode(0o644)
		if file.Executable {
			want = 0o755
		}
		if got := fs.modes[key]; got != want {
			t.Errorf("режим %s: %v, нужно %v", file.Path, got, want)
		}
	}

	agents := fs.files[filepath.Join("/hooks", "AGENTS.md")]
	claude := fs.files[filepath.Join("/hooks", "CLAUDE.md")]
	if string(agents) != string(claude) {
		t.Error("CLAUDE.md не совпадает с AGENTS.md")
	}
	if len(agents) == 0 {
		t.Error("AGENTS.md пуст")
	}
}

func TestBootstrapHooksSecondCallIsNoop(t *testing.T) {
	fs := newFakeHookFS()
	if _, err := BootstrapHooks("/hooks", fs, DefaultHookFiles()); err != nil {
		t.Fatal(err)
	}
	before := make(map[string][]byte, len(fs.files))
	for path, data := range fs.files {
		before[path] = append([]byte(nil), data...)
	}

	written, err := BootstrapHooks("/hooks", fs, DefaultHookFiles())
	if err != nil {
		t.Fatal(err)
	}
	if written != 0 || fs.writes != 9 {
		t.Fatalf("второй вызов: записано %d, всего WriteFile %d", written, fs.writes)
	}
	for path, data := range before {
		if string(fs.files[path]) != string(data) {
			t.Errorf("файл %s изменился при повторном вызове", path)
		}
	}
}

func TestBootstrapHooksKeepsExistingFile(t *testing.T) {
	fs := newFakeHookFS()
	path := filepath.Join("/hooks", "examples", "closed.example")
	fs.files[path] = []byte("#!/bin/sh\necho own hook\n")
	fs.modes[path] = 0o700

	written, err := BootstrapHooks("/hooks", fs, DefaultHookFiles())
	if err != nil {
		t.Fatal(err)
	}
	if written != 8 {
		t.Fatalf("записано %d файлов, нужно 8", written)
	}
	if got := string(fs.files[path]); got != "#!/bin/sh\necho own hook\n" {
		t.Errorf("существующий файл перезаписан: %q", got)
	}
	if got := fs.modes[path]; got != 0o700 {
		t.Errorf("режим существующего файла %v, нужно 0700", got)
	}
}

func TestBootstrapHooksWriteFailure(t *testing.T) {
	fs := newFakeHookFS()
	fs.failWrite = true
	fs.failPath = filepath.Join("/hooks", "README.md")

	written, err := BootstrapHooks("/hooks", fs, DefaultHookFiles())
	if err == nil {
		t.Fatal("ожидалась ошибка записи")
	}
	if !strings.Contains(err.Error(), "README.md") {
		t.Fatalf("ошибка не называет файл: %v", err)
	}
	if written != 2 {
		t.Fatalf("записано до ошибки %d файлов, нужно 2", written)
	}
}

func TestBootstrapHooksRejectsBadInput(t *testing.T) {
	fs := newFakeHookFS()
	if _, err := BootstrapHooks("  ", fs, DefaultHookFiles()); err == nil {
		t.Error("пустой каталог принят")
	}
	if _, err := BootstrapHooks("/hooks", fs, []HookFile{{Path: "../escape"}}); err == nil {
		t.Error("путь с исходной точкой принят")
	}
	if _, err := BootstrapHooks("/hooks", fs, []HookFile{{Path: filepath.Join("examples", "..", "..", "escape")}}); err == nil {
		t.Error("путь вне каталога принят")
	}
}

package worktree

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

type fakeMetaFS struct {
	files     map[string][]byte
	tempData  map[string][]byte
	seq       int
	renameErr error
}

func newFakeMetaFS() *fakeMetaFS {
	return &fakeMetaFS{
		files:    make(map[string][]byte),
		tempData: make(map[string][]byte),
	}
}

func (f *fakeMetaFS) ReadFile(name string) ([]byte, error) {
	data, ok := f.files[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (f *fakeMetaFS) MkdirAll(string, os.FileMode) error { return nil }

func (f *fakeMetaFS) CreateTemp(dir, pattern string) (metaFile, error) {
	f.seq++
	name := filepath.Join(dir, pattern+"tmp-"+strconv.Itoa(f.seq))
	f.tempData[name] = nil
	return &fakeMetaFile{fs: f, name: name}, nil
}

func (f *fakeMetaFS) Rename(old, new string) error {
	if f.renameErr != nil {
		return f.renameErr
	}
	data, ok := f.tempData[old]
	if !ok {
		return os.ErrNotExist
	}
	f.files[new] = append([]byte(nil), data...)
	delete(f.tempData, old)
	return nil
}

func (f *fakeMetaFS) Remove(name string) error {
	delete(f.tempData, name)
	delete(f.files, name)
	return nil
}

type fakeMetaFile struct {
	fs   *fakeMetaFS
	name string
}

func (f *fakeMetaFile) Name() string            { return f.name }
func (f *fakeMetaFile) Chmod(os.FileMode) error { return nil }
func (f *fakeMetaFile) Close() error            { return nil }
func (f *fakeMetaFile) Write(p []byte) (int, error) {
	f.fs.tempData[f.name] = append(f.fs.tempData[f.name], p...)
	return len(p), nil
}

func putRawMeta(t *testing.T, fs *fakeMetaFS, store, worktreeDir string, m Meta) {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	fs.files[MetaPath(store, worktreeDir)] = append(data, '\n')
}

func validMeta(worktreeDir string) Meta {
	return Meta{
		Path:       worktreeDir,
		AgentID:    AgentExecutor,
		PermMode:   PermAcceptEdits,
		Branch:     "fix/cd11",
		MergeGoals: []string{"core", "watch"},
		UpdatedAt:  time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC),
	}
}

func TestMetaRoundTrip(t *testing.T) {
	fs := newFakeMetaFS()
	store := "/state"
	worktreeDir := "/repo/cd11"
	want := validMeta(worktreeDir)

	if err := writeMeta(fs, store, worktreeDir, want); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}
	if _, ok := fs.files[MetaPath(store, worktreeDir)]; !ok {
		t.Fatal("metadata file was not published at MetaPath")
	}
	got, ok, err := readMeta(fs, store, worktreeDir)
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}
	if !ok {
		t.Fatal("readMeta: missing record after write")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestMetaReadRejectsBadRecords(t *testing.T) {
	const (
		store       = "/state"
		worktreeDir = "/repo/cd11"
	)
	tests := []struct {
		name string
		edit func(*Meta)
		want error
	}{
		{"unknown agent", func(m *Meta) { m.AgentID = "stranger" }, ErrMetaAgent},
		{"unknown permission mode", func(m *Meta) { m.PermMode = "sudo" }, ErrMetaPermMode},
		{"foreign path", func(m *Meta) { m.Path = "/repo/other" }, ErrMetaPath},
		{"empty merge goal", func(m *Meta) { m.MergeGoals = []string{"core", ""} }, ErrMetaGoals},
		{"invalid branch", func(m *Meta) { m.Branch = "bad..branch" }, ErrMetaBranch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := newFakeMetaFS()
			m := validMeta(worktreeDir)
			tt.edit(&m)
			putRawMeta(t, fs, store, worktreeDir, m)

			_, ok, err := readMeta(fs, store, worktreeDir)
			if !errors.Is(err, tt.want) {
				t.Fatalf("readMeta error = %v, want %v", err, tt.want)
			}
			if ok {
				t.Fatal("readMeta reported a bad record as present")
			}
		})
	}
}

func TestMetaRenameFailureKeepsPreviousRecord(t *testing.T) {
	fs := newFakeMetaFS()
	const (
		store       = "/state"
		worktreeDir = "/repo/cd11"
	)
	first := validMeta(worktreeDir)
	if err := writeMeta(fs, store, worktreeDir, first); err != nil {
		t.Fatalf("first writeMeta: %v", err)
	}

	second := validMeta(worktreeDir)
	second.AgentID = AgentFixer
	second.UpdatedAt = first.UpdatedAt.Add(time.Hour)
	fs.renameErr = errors.New("simulated break before rename")
	if err := writeMeta(fs, store, worktreeDir, second); err == nil {
		t.Fatal("writeMeta succeeded despite rename failure")
	}
	if len(fs.tempData) != 0 {
		t.Fatalf("temp files left after failed publish: %v", fs.tempData)
	}
	got, ok, err := readMeta(fs, store, worktreeDir)
	if err != nil {
		t.Fatalf("readMeta after break: %v", err)
	}
	if !ok || !reflect.DeepEqual(got, first) {
		t.Fatalf("record after break = %+v (ok=%v), want %+v", got, ok, first)
	}
}

func TestMetaWriteRejectsBadRecords(t *testing.T) {
	fs := newFakeMetaFS()
	const (
		store       = "/state"
		worktreeDir = "/repo/cd11"
	)
	m := validMeta(worktreeDir)
	m.PermMode = "sudo"
	if err := writeMeta(fs, store, worktreeDir, m); !errors.Is(err, ErrMetaPermMode) {
		t.Fatalf("writeMeta error = %v, want %v", err, ErrMetaPermMode)
	}
	if len(fs.files) != 0 {
		t.Fatalf("bad write published a file: %v", fs.files)
	}
}

func TestMetaMissingFile(t *testing.T) {
	fs := newFakeMetaFS()
	_, ok, err := readMeta(fs, "/state", "/repo/cd11")
	if err != nil || ok {
		t.Fatalf("missing file: ok=%v err=%v, want false and nil", ok, err)
	}
}

package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeResumeJournal(t *testing.T, path string, lines []string) {
	t.Helper()
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile journal: %v", err)
	}
}

func readResumeJournal(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile journal: %v", err)
	}
	text := strings.TrimSuffix(string(b), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func TestResumeJournalAppendsToExistingFile(t *testing.T) {
	d := openSessionStore(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.log")
	writeResumeJournal(t, path, []string{"one", "two", "three"})

	if err := d.SaveSession(Session{
		ID:       "ses_resume",
		Status:   SessionStatusActive,
		Metadata: map[string]string{SessionMetaJournalPath: path},
	}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	gotPath, err := d.ResumeJournal("ses_resume", "four")
	if err != nil {
		t.Fatalf("ResumeJournal: %v", err)
	}
	if gotPath != path {
		t.Fatalf("journal path = %q, want %q", gotPath, path)
	}

	lines := readResumeJournal(t, path)
	if len(lines) != 4 {
		t.Fatalf("journal lines = %d, want 4: %v", len(lines), lines)
	}
	if lines[3] != "four" {
		t.Fatalf("appended line = %q, want %q", lines[3], "four")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("journal dir has %d entries, want 1", len(entries))
	}
}

func TestResumeJournalWithoutRecordedPath(t *testing.T) {
	d := openSessionStore(t)
	if err := d.SaveSession(Session{ID: "ses_nopath", Status: SessionStatusActive}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if _, err := d.ResumeJournal("ses_nopath", "entry"); !errors.Is(err, ErrNoJournalPath) {
		t.Fatalf("ResumeJournal error = %v, want ErrNoJournalPath", err)
	}
}

func TestResumeJournalWithMissingFile(t *testing.T) {
	d := openSessionStore(t)
	path := filepath.Join(t.TempDir(), "gone.log")
	if err := d.SaveSession(Session{
		ID:       "ses_gone",
		Status:   SessionStatusActive,
		Metadata: map[string]string{SessionMetaJournalPath: path},
	}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if _, err := d.ResumeJournal("ses_gone", "entry"); !errors.Is(err, ErrJournalPathMissing) {
		t.Fatalf("ResumeJournal error = %v, want ErrJournalPathMissing", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("resume created a missing journal file: %v", err)
	}
}

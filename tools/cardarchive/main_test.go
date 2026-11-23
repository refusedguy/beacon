package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestClosedDoneWithReason(t *testing.T) {
	content := "# T2.2. Output schemas\nСтатус: done\nПричина: migrated\n"
	id, status, detail, ok := closed("T2.2.md", content)
	if !ok || id != "T2.2" || status != "done" || detail != "migrated" {
		t.Fatalf("unexpected card: %q %q %q ok=%v", id, status, detail, ok)
	}
}

func TestClosedSkipsOpenAndMissingStatus(t *testing.T) {
	if _, _, _, ok := closed("x.md", "# X. Topic\nСтатус: todo\n"); ok {
		t.Fatal("open card must be skipped")
	}
	if _, _, _, ok := closed("x.md", "# X. Topic\n"); ok {
		t.Fatal("card without status must be skipped")
	}
}

func TestClosedFallbackToFirstLine(t *testing.T) {
	_, status, detail, ok := closed("BCX-05.md", "# BCX-05. Topic\nСтатус: dropped\n")
	if !ok || status != "dropped" || detail != "# BCX-05. Topic" {
		t.Fatalf("first-line fallback failed: %q %q ok=%v", status, detail, ok)
	}
}

func TestClosedIDFromFilename(t *testing.T) {
	id, _, _, _ := closed("T4.7-rollback-drill.md", "no heading\nСтатус: done\n")
	if id != "T4.7-rollback-drill" {
		t.Fatalf("filename fallback id: %q", id)
	}
}

func TestClosedSanitizesDetail(t *testing.T) {
	_, _, detail, _ := closed("A.md", "# A. T\nСтатус: done\nПричина: a | b\rc\n")
	if detail != "a / b c" {
		t.Fatalf("sanitized detail: %q", detail)
	}
}

func TestGenerateSortedIdempotentAndSkipsOpen(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "docs", "tasks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("T2.md", "# T2. Second\nСтатус: done\nПричина: reason-b\n")
	write("A1.md", "# A1. First\nСтатус: dropped\n")
	write("OPEN.md", "# OPEN. Open\nСтатус: todo\n")
	write("ARCHIVE.md", "stale archive content\n")

	got, err := generate(root)
	if err != nil {
		t.Fatal(err)
	}
	want := "A1 | dropped | # A1. First\nT2 | done | reason-b\n"
	if string(got) != want {
		t.Fatalf("generate: got %q want %q", got, want)
	}
	again, err := generate(root)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, again) {
		t.Fatal("generate is not byte-idempotent")
	}
}

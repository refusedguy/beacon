package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCardFixture(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	return path
}

func assertMarker(t *testing.T, gotPath string, gotLine int, gotFound bool, wantPath string, wantLine int) {
	t.Helper()
	if !gotFound {
		t.Fatalf("CheckCardMarkers did not report a marker, want %s:%d", wantPath, wantLine)
	}
	if gotPath != wantPath {
		t.Fatalf("CheckCardMarkers path = %q, want %q", gotPath, wantPath)
	}
	if gotLine != wantLine {
		t.Fatalf("CheckCardMarkers line = %d, want %d", gotLine, wantLine)
	}
}

func TestCheckCardMarkersFindsMarkerInFrontmatter(t *testing.T) {
	root := t.TempDir()
	wantPath := writeCardFixture(t, root, "T-1.md", strings.Join([]string{
		"---",
		"id: T-1",
		"<<<<<<< Updated upstream",
		"status: available",
		"=======",
		"status: done",
		">>>>>>> Stashed changes",
		"---",
		"# T-1. Card",
	}, "\n"))

	path, line, found := CheckCardMarkers(root)
	assertMarker(t, path, line, found, wantPath, 3)
}

func TestCheckCardMarkersFindsMarkerBelowFrontmatter(t *testing.T) {
	root := t.TempDir()
	wantPath := writeCardFixture(t, root, "T-2.md", strings.Join([]string{
		"---",
		"id: T-2",
		"status: available",
		"---",
		"",
		"# T-2. Card",
		"",
		"Body line.",
		"<<<<<<< HEAD",
		"more",
		"=======",
		"other",
		">>>>>>> branch",
	}, "\n"))

	path, line, found := CheckCardMarkers(root)
	assertMarker(t, path, line, found, wantPath, 9)
}

func TestCheckCardMarkersReportsFirstPathAndLine(t *testing.T) {
	root := t.TempDir()
	wantPath := writeCardFixture(t, root, "T-a.md", strings.Join([]string{
		"line one",
		"line two",
		"<<<<<<< ours",
		"=======",
		">>>>>>> theirs",
	}, "\n"))
	writeCardFixture(t, root, "T-b.md", "<<<<<<< earlier file, later name\n")

	path, line, found := CheckCardMarkers(root)
	assertMarker(t, path, line, found, wantPath, 3)
}

func TestCheckCardMarkersRecognizesAllMarkerPrefixes(t *testing.T) {
	cases := []struct {
		name   string
		marker string
	}{
		{"ours", "<<<<<<< Updated upstream"},
		{"base", "======="},
		{"theirs", ">>>>>>> Stashed changes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			wantPath := writeCardFixture(t, root, "T-3.md", "clean line\n"+tc.marker+"\n")
			path, line, found := CheckCardMarkers(root)
			assertMarker(t, path, line, found, wantPath, 2)
		})
	}
}

func TestCheckCardMarkersCleanCorpusReturnsNoResult(t *testing.T) {
	root := t.TempDir()
	writeCardFixture(t, root, "T-clean.md", strings.Join([]string{
		"---",
		"id: T-clean",
		"status: available",
		"---",
		"",
		"# T-clean. Card",
		"",
		"Body text with `<<<<<<<` in a sentence.",
	}, "\n"))
	writeCardFixture(t, root, "notes.txt", "<<<<<<< not a task card\n=======\n>>>>>>>\n")

	path, line, found := CheckCardMarkers(root)
	if found {
		t.Fatalf("CheckCardMarkers reported %s:%d in a clean corpus", path, line)
	}
	if path != "" || line != 0 {
		t.Fatalf("clean corpus returned path %q and line %d, want empty path and line 0", path, line)
	}
}

func TestCheckCardMarkersDocsTasksCorpusIsClean(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "tasks")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("docs/tasks corpus is not available: %v", err)
	}
	path, line, found := CheckCardMarkers(root)
	if found {
		t.Fatalf("docs/tasks corpus has a conflict marker at %s:%d", path, line)
	}
}

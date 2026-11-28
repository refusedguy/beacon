package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// docPaths lists the files whose marker blocks beacon-gen owns.
var docPaths = []string{
	"AGENTS.md",
	"README.md",
	".opencode/plugins/beacon-project/rules/context.js",
}

// TestDocsUpToDate copies the committed docs to a temp repo, runs the same
// generator go generate calls, and diffs the result. A stale tool list, count
// or action name fails here instead of reaching a worker's permission check.
func TestDocsUpToDate(t *testing.T) {
	tmp := t.TempDir()
	for _, rel := range docPaths {
		committed, err := os.ReadFile(filepath.Join("../..", rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		path := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(path, committed, 0o600); err != nil {
			t.Fatalf("write temp %s: %v", rel, err)
		}
	}

	cmd := exec.Command("go", "run", ".", "-docs", "-repo", tmp)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run beacon-gen -docs: %v\n%s", err, stderr.String())
	}

	for _, rel := range docPaths {
		want, err := os.ReadFile(filepath.Join("../..", rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		got, err := os.ReadFile(filepath.Join(tmp, rel))
		if err != nil {
			t.Fatalf("read generated %s: %v", rel, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale. Run `go generate ./cmd/beacon-gen` and commit the result.\nwant:\n%s\ngot:\n%s", rel, want, got)
		}
	}
}

// TestOrchestratorAgentHasNoUnprefixedActions keeps the removed frontmatter
// permissions from coming back. opencode.json is the generated permission
// source and its actions carry the beacon_beacon_ prefix.
func TestOrchestratorAgentHasNoUnprefixedActions(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("../..", ".opencode", "agents", "orchestrator.md"))
	if err != nil {
		t.Fatalf("read orchestrator.md: %v", err)
	}
	if strings.Contains(string(data), "action:") {
		t.Errorf("orchestrator.md declares permissions by hand:\n%s", data)
	}
}

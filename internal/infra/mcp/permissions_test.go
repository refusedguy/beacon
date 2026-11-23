package mcp

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPermissionsUpToDate guards against drift between the committed
// opencode.json and the permissions generated from ToolCatalog. It copies the
// committed file to a temp path, runs the generator on that copy and fails if
// the result differs, so CI catches a catalog change without regeneration.
func TestPermissionsUpToDate(t *testing.T) {
	const committed = "../../../opencode.json"

	want, err := os.ReadFile(committed)
	if err != nil {
		t.Fatalf("read %s: %v", committed, err)
	}

	tmp := filepath.Join(t.TempDir(), "opencode.json")
	if err := os.WriteFile(tmp, want, 0o600); err != nil {
		t.Fatalf("write temp copy: %v", err)
	}

	cmd := exec.Command("go", "run", "../../../cmd/beacon-gen", "-out", tmp)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run beacon-gen: %v\n%s", err, stderr.String())
	}

	got, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("read generated copy: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is stale. Run `go generate ./...` and commit the result.\nwant:\n%s\ngot:\n%s", committed, want, got)
	}
}

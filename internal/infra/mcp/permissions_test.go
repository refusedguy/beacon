package mcp

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

func TestGeneratedPermissionsUseRealActionNames(t *testing.T) {
	var names []string
	for _, spec := range ToolCatalog() {
		if spec.OrchestratorOnly {
			names = append(names, spec.Name)
		}
	}
	sort.Strings(names)
	set := GeneratedPermissions(ToolCatalog())
	for _, group := range []struct {
		name   string
		perms  []Permission
		effect string
	}{
		{"global", set.Global, "deny"},
		{"allow", set.Allow, "allow"},
		{"deny", set.Deny, "deny"},
	} {
		if len(group.perms) != len(names) {
			t.Fatalf("%s has %d entries, want %d", group.name, len(group.perms), len(names))
		}
		for i, perm := range group.perms {
			if perm.Action != names[i] {
				t.Errorf("%s[%d] action = %q, want %q", group.name, i, perm.Action, names[i])
			}
			if perm.Effect != group.effect {
				t.Errorf("%s[%d] effect = %q, want %q", group.name, i, perm.Effect, group.effect)
			}
		}
	}
}

func TestMergePermissionsDropsLegacyActionNames(t *testing.T) {
	want := GeneratedPermissions(ToolCatalog()).Global
	legacy := map[string]any{"action": "beacon_beacon_task_create", "effect": "deny", "resource": "*"}
	custom := map[string]any{"action": "custom", "effect": "allow", "resource": "*"}

	got := MergePermissions([]any{legacy, custom}, want)
	foundLegacy, foundCustom := false, false
	for _, entry := range got {
		obj, _ := entry.(map[string]any)
		switch obj["action"] {
		case legacy["action"]:
			foundLegacy = true
		case custom["action"]:
			foundCustom = true
		}
	}
	if foundLegacy {
		t.Error("legacy beacon_beacon_ entry survived rewrite")
	}
	if !foundCustom {
		t.Error("custom permission was dropped")
	}
}

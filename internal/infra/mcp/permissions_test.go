package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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

// canonicalJSON decodes and re-encodes a document so two writers that emit
// structs and maps in different field orders can be compared byte for byte.
func canonicalJSON(t *testing.T, data []byte) []byte {
	t.Helper()
	var value any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		t.Fatalf("decode JSON for comparison: %v", err)
	}
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode JSON for comparison: %v", err)
	}
	return out
}

// TestGeneratorMatchesLibraryPermissions feeds one document through both
// permission writers and diffs the bytes. The document carries a user
// permission that both writers must keep and a stale beacon_beacon_task_create
// entry that both must drop. A rule changed in one writer and not the other
// fails here, so the owned-action rule cannot drift between them.
func TestGeneratorMatchesLibraryPermissions(t *testing.T) {
	const committed = "../../../opencode.json"
	base, err := os.ReadFile(committed)
	if err != nil {
		t.Fatalf("read %s: %v", committed, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(base, &doc); err != nil {
		t.Fatalf("decode %s: %v", committed, err)
	}

	custom := map[string]any{"action": "custom", "effect": "allow", "resource": "*"}
	legacy := map[string]any{"action": "beacon_beacon_task_create", "effect": "deny", "resource": "*"}
	inject := func(value any) any {
		list, _ := value.([]any)
		return append([]any{custom, legacy}, list...)
	}
	if doc["permissions"] == nil {
		doc["permissions"] = []any{}
	}
	doc["permissions"] = inject(doc["permissions"])
	agents, _ := doc["agents"].(map[string]any)
	for name := range agents {
		agent, _ := agents[name].(map[string]any)
		if agent == nil {
			continue
		}
		if agent["permissions"] == nil {
			agent["permissions"] = []any{}
		}
		agent["permissions"] = inject(agent["permissions"])
	}
	input, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("encode input document: %v", err)
	}
	input = append(input, '\n')

	// Library writer: apply the catalog-derived set with the role mapping
	// cmd/beacon-gen uses.
	set := GeneratedPermissions(ToolCatalog())
	libDoc := map[string]any{}
	if err := json.Unmarshal(input, &libDoc); err != nil {
		t.Fatalf("decode input copy: %v", err)
	}
	libDoc["permissions"] = MergePermissions(libDoc["permissions"], set.Global)
	libAgents, _ := libDoc["agents"].(map[string]any)
	for name, want := range map[string][]Permission{
		"orchestrator": set.Allow,
		"build":        set.Allow,
		"worker":       set.Deny,
	} {
		agent, ok := libAgents[name].(map[string]any)
		if !ok {
			continue
		}
		agent["permissions"] = MergePermissions(agent["permissions"], want)
	}
	wantBytes, err := json.MarshalIndent(libDoc, "", "  ")
	if err != nil {
		t.Fatalf("encode library output: %v", err)
	}

	// Generator writer: run cmd/beacon-gen on the same document bytes.
	tmp := filepath.Join(t.TempDir(), "opencode.json")
	if err := os.WriteFile(tmp, input, 0o600); err != nil {
		t.Fatalf("write temp document: %v", err)
	}
	cmd := exec.Command("go", "run", "../../../cmd/beacon-gen", "-out", tmp)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run beacon-gen: %v\n%s", err, stderr.String())
	}
	gotBytes, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("read generated document: %v", err)
	}

	if !bytes.Equal(canonicalJSON(t, gotBytes), canonicalJSON(t, wantBytes)) {
		t.Errorf("permission writers disagree\nlibrary:\n%s\ngenerator:\n%s", wantBytes, gotBytes)
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

// TestToolSpecTableIsSingleSource proves the one registration table is
// complete and that its derived views agree. A tool added without a table row,
// or with a missing register function, fails here.
func TestToolSpecTableIsSingleSource(t *testing.T) {
	if len(toolSpecs) == 0 {
		t.Fatal("toolSpecs is empty")
	}
	seen := make(map[string]bool, len(toolSpecs))
	for i, spec := range toolSpecs {
		if spec.Name == "" {
			t.Fatalf("toolSpecs[%d] has no name", i)
		}
		if !strings.HasPrefix(spec.Name, "beacon_") {
			t.Errorf("toolSpecs[%d] name %q lacks the beacon_ prefix", i, spec.Name)
		}
		if seen[spec.Name] {
			t.Errorf("toolSpecs has duplicate name %q", spec.Name)
		}
		seen[spec.Name] = true
		if spec.Register == nil {
			t.Errorf("toolSpecs[%d] %q has no register function", i, spec.Name)
		}
		tool := catalogTool(spec.Name)
		if tool.Name != spec.Name || tool.Description != spec.Description {
			t.Errorf("catalogTool(%q) = %+v, want name and description from the table", spec.Name, tool)
		}
	}
	if len(toolRegistrations) != len(toolSpecs) {
		t.Fatalf("toolRegistrations has %d entries, toolSpecs has %d", len(toolRegistrations), len(toolSpecs))
	}
	catalog := ToolCatalog()
	if len(catalog) != len(toolSpecs) {
		t.Fatalf("ToolCatalog has %d entries, toolSpecs has %d", len(catalog), len(toolSpecs))
	}
	for i, spec := range catalog {
		want := toolSpecs[i]
		if spec.Name != want.Name || spec.Description != want.Description || spec.OrchestratorOnly != want.OrchestratorOnly {
			t.Errorf("ToolCatalog[%d] = %+v, want %+v", i, spec, want)
		}
	}
}

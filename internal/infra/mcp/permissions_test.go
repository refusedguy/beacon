package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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

// TestRegenerationKeepsNamespacedActions proves a rewrite restores a namespaced
// action instead of dropping it as owned. It also proves the stale unprefixed
// name from the previous generator is removed.
func TestRegenerationKeepsNamespacedActions(t *testing.T) {
	const committed = "../../../opencode.json"
	base, err := os.ReadFile(committed)
	if err != nil {
		t.Fatalf("read %s: %v", committed, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(base, &doc); err != nil {
		t.Fatalf("decode %s: %v", committed, err)
	}

	namespaced := map[string]any{"action": "beacon_beacon_task_create", "effect": "allow", "resource": "*"}
	stale := map[string]any{"action": "beacon_task_create", "effect": "allow", "resource": "*"}
	list, _ := doc["permissions"].([]any)
	doc["permissions"] = append([]any{namespaced, stale}, list...)
	input, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("encode input document: %v", err)
	}
	input = append(input, '\n')

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
	got, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("read generated document: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("decode generated document: %v", err)
	}
	effects := map[string]string{}
	if entries, ok := out["permissions"].([]any); ok {
		for _, entry := range entries {
			obj, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			action, _ := obj["action"].(string)
			effect, _ := obj["effect"].(string)
			effects[action] = effect
		}
	}
	if effects["beacon_beacon_task_create"] != "deny" {
		t.Errorf("namespaced action was not kept: %v", effects)
	}
	if _, ok := effects["beacon_task_create"]; ok {
		t.Errorf("stale unprefixed action survived regeneration: %v", effects)
	}
}

// actionEffects indexes a permission list by action for order-free checks.
func actionEffects(perms []Permission) map[string]string {
	out := make(map[string]string, len(perms))
	for _, p := range perms {
		out[p.Action] = p.Effect
	}
	return out
}

// TestGeneratedPermissionsUseNamespacedActions pins the exact action strings
// OpenCode checks. The CLI joins the server name and the MCP tool name, so the
// beacon server turns beacon_task_create into beacon_beacon_task_create.
func TestGeneratedPermissionsUseNamespacedActions(t *testing.T) {
	set := GeneratedPermissions(ToolCatalog())
	wantGlobal := []string{
		"beacon_beacon_control",
		"beacon_beacon_dispatch",
		"beacon_beacon_recent",
		"beacon_beacon_reconcile",
		"beacon_beacon_status",
		"beacon_beacon_task_close",
		"beacon_beacon_task_create",
		"beacon_beacon_task_delete",
		"beacon_beacon_task_start",
		"beacon_beacon_task_update",
	}
	global := actionEffects(set.Global)
	if len(global) != len(wantGlobal) {
		t.Fatalf("global has %d entries, want %d: %v", len(global), len(wantGlobal), global)
	}
	for _, action := range wantGlobal {
		if global[action] != "deny" {
			t.Errorf("global %q = %q, want deny", action, global[action])
		}
	}
	for _, action := range []string{
		"beacon_control", "beacon_dispatch", "beacon_recent", "beacon_reconcile",
		"beacon_status", "beacon_task_close", "beacon_task_create",
		"beacon_task_delete", "beacon_task_start", "beacon_task_update",
	} {
		if _, ok := global[action]; ok {
			t.Errorf("global contains unprefixed %q", action)
		}
	}
}

// TestGeneratedPermissionsDenyWorkerControl keeps run control with the
// orchestrator. Build and orchestrator get an allow; workers get a deny.
func TestGeneratedPermissionsDenyWorkerControl(t *testing.T) {
	set := GeneratedPermissions(ToolCatalog())
	if got := actionEffects(set.Allow)["beacon_beacon_control"]; got != "allow" {
		t.Errorf("allow beacon_beacon_control = %q, want allow", got)
	}
	if got := actionEffects(set.Deny)["beacon_beacon_control"]; got != "deny" {
		t.Errorf("deny beacon_beacon_control = %q, want deny", got)
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
// permission that both writers must keep and a stale unprefixed beacon_task_create
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
	legacy := map[string]any{"action": "beacon_task_create", "effect": "deny", "resource": "*"}
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
	legacy := map[string]any{"action": "beacon_task_create", "effect": "deny", "resource": "*"}
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
		t.Error("legacy unprefixed beacon_ entry survived rewrite")
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

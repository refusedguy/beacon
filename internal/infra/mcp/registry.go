package mcp

import (
	"sort"
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
)

// ToolCatalog is the single declarative source for the tool permissions in
// opencode.json. Running go generate rewrites those permissions from it.
//go:generate go run ../../../cmd/beacon-gen -out ../../../opencode.json

// ToolSpec describes one MCP tool for consumers such as a permissions generator.
type ToolSpec struct {
	Name             string
	Description      string
	OrchestratorOnly bool
}

// ToolCatalog is the declarative source of the tools this server exposes.
// Each entry matches the msdk.Tool owned by the matching tool file.
func ToolCatalog() []ToolSpec {
	return []ToolSpec{
		{Name: "beacon_dispatch", Description: "Pick up to N non-overlapping tasks and start agents."},
		{Name: "beacon_watch_pr", Description: "Bind a pull request to a running task."},
		{Name: "beacon_status", Description: "List active runs, PRs and blockers."},
		{Name: "beacon_recent", Description: "List the most recently finished runs."},
		{Name: "beacon_wait", Description: "Wait for a run to reach an active verdict or a terminal state."},
		{Name: "beacon_control", Description: controlDescription()},
		{Name: "beacon_task_create", Description: "Create and publish a task card. Orchestrator only.", OrchestratorOnly: true},
		{Name: "beacon_task_delete", Description: "Delete task cards in one commit and push. Orchestrator only.", OrchestratorOnly: true},
		{Name: "beacon_task_update", Description: "Update task cards in one commit and push. Orchestrator only.", OrchestratorOnly: true},
		{Name: "beacon_reconcile", Description: "Force one reconciler tick for a PR and report its decision. Orchestrator only.", OrchestratorOnly: true},
	}
}

// controlDescription spells out the verbs from core, so the advertised
// vocabulary cannot lose a verb when the core list changes.
func controlDescription() string {
	verbs := core.ControlActionNames()
	switch len(verbs) {
	case 0:
		return "Control a run."
	case 1:
		return "Control a run: " + verbs[0] + "."
	default:
		return "Control a run: " + strings.Join(verbs[:len(verbs)-1], ", ") + " or " + verbs[len(verbs)-1] + "."
	}
}

// Permission is one entry in an opencode.json permission list.
type Permission struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}

// PermissionSet groups the catalog-derived entries by scope. Global denies an
// orchestrator-only tool everywhere, Allow restores it for orchestrators and
// the build agent, and Deny keeps it away from workers.
type PermissionSet struct {
	Global []Permission
	Allow  []Permission
	Deny   []Permission
}

// GeneratedPermissions derives the permission entries beacon-gen owns from the
// tool catalog. Catalog names already carry their prefix, so each
// orchestrator-only tool contributes exactly one real action name.
func GeneratedPermissions(specs []ToolSpec) PermissionSet {
	set := PermissionSet{}
	sorted := append([]ToolSpec(nil), specs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, spec := range sorted {
		if !spec.OrchestratorOnly {
			continue
		}
		set.Global = append(set.Global, permission(spec.Name, "deny"))
		set.Allow = append(set.Allow, permission(spec.Name, "allow"))
		set.Deny = append(set.Deny, permission(spec.Name, "deny"))
	}
	return set
}

func permission(action, effect string) Permission {
	return Permission{Action: action, Resource: "*", Effect: effect}
}

// MergePermissions drops every entry owned by the catalog, then prepends a
// fresh ordered set. Entries the catalog does not own keep their value.
func MergePermissions(value any, want []Permission) []any {
	owned := make(map[string]bool, len(want)*2)
	for _, p := range want {
		owned[p.Action] = true
		// Older generators prefixed catalog names a second time. Drop those
		// stale entries as well so a rewrite converges on the current names.
		owned["beacon_"+p.Action] = true
	}
	out := make([]any, 0, len(want))
	for _, p := range want {
		out = append(out, p)
	}
	list, _ := value.([]any)
	for _, entry := range list {
		obj, ok := entry.(map[string]any)
		if !ok {
			out = append(out, entry)
			continue
		}
		action, _ := obj["action"].(string)
		if !owned[action] {
			out = append(out, entry)
		}
	}
	return out
}

// PermissionsInSync reports whether every owned action appears with the wanted
// effect in a decoded permission list.
func PermissionsInSync(value any, want []Permission) bool {
	have := make(map[string]string)
	if list, ok := value.([]any); ok {
		for _, entry := range list {
			obj, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			action, _ := obj["action"].(string)
			effect, _ := obj["effect"].(string)
			if action != "" {
				have[action] = effect
			}
		}
	}
	for _, p := range want {
		if have[p.Action] != p.Effect {
			return false
		}
	}
	return true
}

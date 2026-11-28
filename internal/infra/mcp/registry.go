package mcp

import (
	"sort"
	"strings"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/pkg/core"
)

// ToolCatalog is the single declarative source for the tool permissions in
// opencode.json. Running go generate rewrites those permissions from it.
//go:generate go run ../../../cmd/beacon-gen -out ../../../opencode.json

// ToolSpec describes one MCP tool. Catalog metadata and the register function
// live in the same value, so adding a tool needs one table entry.
type ToolSpec struct {
	Name        string
	Description string
	// OrchestratorOnly denies a tool to workers in opencode.json. Workers
	// inherit the pooled MCP daemon without call metadata, so the daemon is a
	// trusted boundary and the client permission is the role gate. A caller
	// that does declare the worker role is still refused server-side.
	OrchestratorOnly bool
	// AllowAll grants a worker tool to every agent, so a normal call never
	// falls back to an interactive permission prompt.
	AllowAll bool
	Register func(*msdk.Server, Services)
}

const (
	roleAbsent       = "absent"
	roleOrchestrator = "orchestrator"
)

// requireOrchestrator refuses a tool call that does not name an orchestrator
// agent. A worker identity and absent agent metadata are both refusals, each
// with a coded error that names the observed role and the required role. A
// caller can then diagnose the denial from the error alone instead of falling
// through to an interactive permission prompt.
func requireOrchestrator(req *msdk.CallToolRequest) error {
	agent := metaString(req, "beacon/agent", "ai.opencode/agent")
	observed := strings.TrimSpace(agent)
	if observed == "" {
		observed = roleAbsent
	}
	if observed == roleAbsent || isWorkerAgent(observed) {
		return core.Errorf(core.CodePermissionDenied,
			"tool is orchestrator only; observed role %q, required role %q", observed, roleOrchestrator)
	}
	return nil
}

func isWorkerAgent(agent string) bool {
	name := strings.ToLower(strings.TrimSpace(agent))
	return name == "worker" || strings.HasSuffix(name, "-worker")
}

// toolSpecs is the one registration table. NewServer walks its derived
// register list and ToolCatalog projects it for the permissions generator. Each
// function owns exactly one tool implementation in its own file.
var toolSpecs []ToolSpec

// toolRegistrations is the register-function view of the one table. It is
// derived here, never maintained by hand, so NewServer and the tests read the
// same entries without a second place to add a tool.
var toolRegistrations []func(*msdk.Server, Services)

// init fills both views in one place. A variable initializer that named the
// register functions would form an initialization cycle: those functions call
// catalogTool, which reads the table.
func init() {
	toolSpecs = []ToolSpec{
		{Name: "beacon_dispatch", Description: "Pick up to N non-overlapping tasks and start agents.", Register: registerDispatch},
		{Name: "beacon_watch_pr", Description: "Bind a pull request to a running task.", Register: registerWatchPR},
		{Name: "beacon_status", Description: "List active runs, PRs and blockers.", Register: registerStatus},
		{Name: "beacon_recent", Description: "List the most recently finished runs.", Register: registerRecent},
		{Name: "beacon_wait", Description: "Wait for a run to reach an active verdict or a terminal state.", Register: registerWait},
		{Name: "beacon_control", Description: controlDescription(), AllowAll: true, Register: registerControl},
		{Name: "beacon_task_create", Description: "Create and publish a task card. Orchestrator only.", OrchestratorOnly: true, Register: registerTaskCreate},
		{Name: "beacon_task_delete", Description: "Delete task cards in one commit and push. Orchestrator only.", OrchestratorOnly: true, Register: registerTaskDelete},
		{Name: "beacon_task_update", Description: "Update task cards in one commit and push. Orchestrator only.", OrchestratorOnly: true, Register: registerTaskUpdate},
		{Name: "beacon_task_close", Description: "Close task cards with a status and reason in one commit and push. Orchestrator only.", OrchestratorOnly: true, Register: registerTaskClose},
		{Name: "beacon_task_start", Description: "Start one specific task by ID. Orchestrator only.", OrchestratorOnly: true, Register: registerTaskStart},
		{Name: "beacon_reconcile", Description: "Force one reconciler tick for a PR and report its decision. Orchestrator only.", OrchestratorOnly: true, Register: registerReconcile},
	}
	toolRegistrations = make([]func(*msdk.Server, Services), 0, len(toolSpecs))
	for _, spec := range toolSpecs {
		toolRegistrations = append(toolRegistrations, spec.Register)
	}
}

// ToolCatalog returns the registration table as catalog entries. The copy keeps
// callers from editing the table in place.
func ToolCatalog() []ToolSpec {
	return append([]ToolSpec(nil), toolSpecs...)
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
// the build agent, and Deny keeps it away from workers. Worker tools marked
// AllowAll appear in Allow and Deny with the allow effect.
type PermissionSet struct {
	Global []Permission
	Allow  []Permission
	Deny   []Permission
}

// mcpAction is the action string OpenCode checks for an MCP tool. The CLI
// joins the server name and the tool name with an underscore, so the beacon
// server and a beacon_* tool produce a beacon_beacon_* action.
func mcpAction(name string) string {
	return "beacon_" + name
}

// GeneratedPermissions derives the permission entries beacon-gen owns from the
// tool catalog. OpenCode prefixes every tool name with the server name, so a
// catalog name such as beacon_task_create becomes beacon_beacon_task_create.
func GeneratedPermissions(specs []ToolSpec) PermissionSet {
	set := PermissionSet{}
	sorted := append([]ToolSpec(nil), specs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, spec := range sorted {
		action := mcpAction(spec.Name)
		switch {
		case spec.OrchestratorOnly:
			set.Global = append(set.Global, permission(action, "deny"))
			set.Allow = append(set.Allow, permission(action, "allow"))
			set.Deny = append(set.Deny, permission(action, "deny"))
		case spec.AllowAll:
			set.Allow = append(set.Allow, permission(action, "allow"))
			set.Deny = append(set.Deny, permission(action, "allow"))
		}
	}
	return set
}

func permission(action, effect string) Permission {
	return Permission{Action: action, Resource: "*", Effect: effect}
}

// OwnedActions returns the action names owned by the generated permission
// writer. It keeps the namespaced action this generator writes and also drops
// the unprefixed catalog name older generators emitted. Keeping the rule here
// gives the library merge and cmd/beacon-gen one owner.
func OwnedActions(want []Permission) map[string]bool {
	owned := make(map[string]bool, len(want)*2)
	for _, p := range want {
		owned[p.Action] = true
		owned[strings.TrimPrefix(p.Action, "beacon_")] = true
	}
	return owned
}

// MergePermissions drops every entry owned by the catalog, then prepends a
// fresh ordered set. Entries the catalog does not own keep their value.
func MergePermissions(value any, want []Permission) []any {
	owned := OwnedActions(want)
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

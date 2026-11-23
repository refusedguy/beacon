package mcp

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
		{Name: "beacon_control", Description: "Control a run: approve, unblock, cancel, retry or bind a PR."},
		{Name: "beacon_task_create", Description: "Create and publish a task card. Orchestrator only.", OrchestratorOnly: true},
		{Name: "beacon_task_delete", Description: "Delete task cards in one commit and push. Orchestrator only.", OrchestratorOnly: true},
	}
}

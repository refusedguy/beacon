package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/refusedguy/beacon/internal/infra/config"
	beaconmcp "github.com/refusedguy/beacon/internal/infra/mcp"
)

// buildInitConfig constructs the Beacon config from resolved options and the remote.
func buildInitConfig(root string, o initOptions, r remote, base string) config.Config {
	provider := firstNonEmpty(o.scm, r.provider)
	tokenEnv := "GITHUB_TOKEN"
	baseURL := ""
	if provider == "gitlab" {
		tokenEnv = "GITLAB_TOKEN"
		baseURL = gitlabAPI(r.host)
	}
	stack := detectStack(root)
	return config.Config{
		StateDB:      ".beacon/state.db",
		WorkspaceDir: ".beacon/worktrees",
		BaseBranch:   base,
		PollInterval: "30s",
		Concurrency: config.Concurrency{
			MaxInFlight: o.maxInFlight, MaxActiveAgents: o.maxActiveAgents,
		},
		TaskSource: config.TaskSource{
			Provider: o.taskSource, Dir: "docs/tasks", Owner: r.owner, Repo: r.repo, TokenEnv: tokenEnv,
		},
		SCM: config.SCM{
			Provider: provider, Owner: r.owner, Repo: r.repo, TokenEnv: tokenEnv, BaseURL: baseURL,
		},
		Harness: config.Harness{
			Provider: o.harness, Endpoint: o.endpoint, Command: "worker",
			WorkerInstructions: stack.workerInstructions(o.weakPC),
		},
		Merge: config.Merge{
			RequiredChecksMode: config.RequiredChecksWorkflow,
			RequiredChecks:     detectRequiredChecks(root),
			MaxAttempts:        12, AutoRebase: true, Labels: []string{},
		},
		OnBaseRed: config.OnBaseRed{Mode: "spawn_fixer", MaxFixAttempts: 2},
	}
}

// gitlabAPI returns the GitLab API base URL for host.
func gitlabAPI(host string) string {
	if host == "" || host == "gitlab.com" {
		return "https://gitlab.com/api/v4"
	}
	return "https://" + host + "/api/v4"
}

// writeInitArtifacts writes the config, tasks directory, gitignore, opencode config and plugin.
func writeInitArtifacts(root string, cfg config.Config, o initOptions) error {
	path := filepath.Join(root, ".beacon", "config.yaml")
	if err := config.WriteDefault(path, cfg, o.force); err != nil {
		return err
	}
	if err := ensureTasksDir(root); err != nil {
		return err
	}
	if err := appendGitignore(filepath.Join(root, ".gitignore")); err != nil {
		return err
	}
	if err := mergeOpenCodeConfig(root); err != nil {
		return err
	}
	return deployPlugin(root, o)
}

// ensureTasksDir creates docs/tasks and its .gitkeep marker.
func ensureTasksDir(root string) error {
	dir := filepath.Join(root, "docs", "tasks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, ".gitkeep")
	if existsIn(dir, ".gitkeep") {
		return nil
	}
	return os.WriteFile(path, nil, 0o644)
}

// detectRequiredChecks collects job names from CI workflow files. The parser
// lives in the config package so init and config load resolve the same names.
func detectRequiredChecks(root string) []string {
	return config.DetectRequiredChecks(root)
}

// mergeOpenCodeConfig adds agents, permissions and the beacon MCP server to opencode.json.
func mergeOpenCodeConfig(root string) error {
	path := filepath.Join(root, "opencode.json")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if os.IsNotExist(err) {
		data = []byte("{}")
	}
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return fmt.Errorf("opencode.json: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	gen := beaconmcp.GeneratedPermissions(beaconmcp.ToolCatalog())
	agents, _ := doc["agents"].(map[string]any)
	if agents == nil {
		agents = map[string]any{}
		doc["agents"] = agents
	}
	for name, role := range roleDefinitions(gen) {
		agent, ok := agents[name].(map[string]any)
		if !ok {
			agent = map[string]any{"description": role.description}
			if role.mode != "" {
				agent["mode"] = role.mode
			}
		}
		agent["permissions"] = beaconmcp.MergePermissions(agent["permissions"], role.perms)
		agents[name] = agent
	}
	doc["permissions"] = beaconmcp.MergePermissions(doc["permissions"], gen.Global)
	mergeMCPServer(doc, root)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// roleDefinition describes one generated operator role.
type roleDefinition struct {
	description string
	mode        string
	perms       []beaconmcp.Permission
}

// roleDefinitions maps role names to their description, mode and permissions.
func roleDefinitions(gen beaconmcp.PermissionSet) map[string]roleDefinition {
	return map[string]roleDefinition{
		"orchestrator": {"Creates and dispatches Beacon tasks.", "all", gen.Allow},
		"build":        {"Default coding agent allowed to manage Beacon tasks.", "", gen.Allow},
		"worker":       {"Executes one Beacon task in its own worktree.", "subagent", gen.Deny},
	}
}

// mergeMCPServer installs the pooled beacon MCP server entry in doc.
func mergeMCPServer(doc map[string]any, root string) {
	mcpMap, _ := doc["mcp"].(map[string]any)
	if mcpMap == nil {
		mcpMap = map[string]any{}
		doc["mcp"] = mcpMap
	}
	server, _ := mcpMap["beacon"].(map[string]any)
	if server == nil {
		server = map[string]any{}
	}
	server["type"] = "local"
	server["command"] = []any{"beacon", "mcp"}
	server["environment"] = map[string]any{"BEACON_PROJECT_ROOT": root}
	server["pool"] = true
	mcpMap["beacon"] = server
}

// appendGitignore adds Beacon artifacts to the gitignore file.
func appendGitignore(path string) error {
	entries := []string{
		".beacon/state.db*",
		".beacon/worktrees/",
		".beacon/connection.json",
		".beacon/*.lock",
		".beacon/daemon*",
		".beacon/config.yaml",
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(string(data), "\n")
	have := make(map[string]bool)
	for _, line := range lines {
		have[strings.TrimSpace(line)] = true
	}
	var add []string
	for _, e := range entries {
		if !have[e] {
			add = append(add, e)
		}
	}
	if len(add) == 0 {
		return nil
	}
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	lines = append(lines, add...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

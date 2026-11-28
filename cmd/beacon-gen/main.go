// Command beacon-gen regenerates the opencode.json tool permissions from the
// MCP tool catalog. It rewrites only the permission entries owned by the
// generator and leaves every other config key, agent description, mode and
// custom permission untouched.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/refusedguy/beacon/internal/infra/mcp"
)

// generated holds the permission entries the generator owns, grouped by the
// scope they belong to. It denies an orchestrator-only tool globally and for
// workers, allows it for the orchestrator and build agents, and allows worker
// tools for every agent.
type generated struct {
	global  []any
	allow   []any
	worker  []any
	actions map[string]bool
}

func main() {
	out := flag.String("out", "", "path to the opencode.json file to rewrite in place")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "beacon-gen: -out is required")
		os.Exit(2)
	}

	data, err := os.ReadFile(*out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "beacon-gen: %v\n", err)
		os.Exit(1)
	}

	result, err := generate(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "beacon-gen: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(*out, result, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "beacon-gen: %v\n", err)
		os.Exit(1)
	}
}

func generate(data []byte) ([]byte, error) {
	var root map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	gen := buildGenerated(mcp.ToolCatalog())
	root["permissions"] = rewritePermissions(root["permissions"], gen, gen.global)

	agents, _ := root["agents"].(map[string]any)
	for name, want := range map[string][]any{
		"orchestrator": gen.allow,
		"build":        gen.allow,
		"worker":       gen.worker,
	} {
		agent, ok := agents[name].(map[string]any)
		if !ok {
			continue
		}
		agent["permissions"] = rewritePermissions(agent["permissions"], gen, want)
	}

	result, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return append(result, '\n'), nil
}

func buildGenerated(specs []mcp.ToolSpec) generated {
	// The catalog package owns the action names and the role mapping. Deriving
	// every list from it keeps this writer in step with the library merge.
	set := mcp.GeneratedPermissions(specs)
	owned := make([]mcp.Permission, 0, len(set.Global)+len(set.Allow)+len(set.Deny))
	owned = append(owned, set.Global...)
	owned = append(owned, set.Allow...)
	owned = append(owned, set.Deny...)
	return generated{
		global:  permissionMaps(set.Global),
		allow:   permissionMaps(set.Allow),
		worker:  permissionMaps(set.Deny),
		actions: mcp.OwnedActions(owned),
	}
}

func permissionMaps(perms []mcp.Permission) []any {
	out := make([]any, 0, len(perms))
	for _, p := range perms {
		out = append(out, map[string]any{
			"action":   p.Action,
			"resource": p.Resource,
			"effect":   p.Effect,
		})
	}
	return out
}

// rewritePermissions drops every entry whose action is owned by the generator,
// then prepends a fresh, ordered set of generated entries. Entries the
// generator does not own keep their original decoded value unchanged.
func rewritePermissions(value any, gen generated, want []any) []any {
	out := append([]any(nil), want...)
	list, _ := value.([]any)
	for _, entry := range list {
		obj, ok := entry.(map[string]any)
		if !ok {
			out = append(out, entry)
			continue
		}
		action, _ := obj["action"].(string)
		if gen.actions[action] {
			continue
		}
		out = append(out, entry)
	}
	return out
}

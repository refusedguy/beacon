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
	"sort"

	"github.com/refusedguy/beacon/internal/infra/mcp"
)

// generated holds the permission entries the generator owns, grouped by the
// scope they belong to. The generator denies an orchestrator-only tool globally
// and for workers, and allows it for the orchestrator and build agents.
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
	g := generated{actions: make(map[string]bool)}
	sorted := append([]mcp.ToolSpec(nil), specs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	for _, spec := range sorted {
		if !spec.OrchestratorOnly {
			continue
		}
		g.global = append(g.global, permission(spec.Name, "deny"))
		g.allow = append(g.allow, permission(spec.Name, "allow"))
		g.worker = append(g.worker, permission(spec.Name, "deny"))
	}
	// The owned-action rule, including the stale doubled prefix, belongs to
	// the catalog package. This generator and the library merge now share it.
	g.actions = mcp.OwnedActions(mcp.GeneratedPermissions(sorted).Global)
	return g
}

func permission(action, effect string) map[string]any {
	return map[string]any{
		"action":   action,
		"resource": "*",
		"effect":   effect,
	}
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

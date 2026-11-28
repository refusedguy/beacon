// Command beacon-gen regenerates the opencode.json tool permissions from the
// MCP tool catalog. It rewrites only the permission entries owned by the
// generator and leaves every other config key, agent description, mode and
// custom permission untouched. With -docs it rewrites the catalog-derived
// marker blocks in AGENTS.md, README.md and the project plugin rules.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/refusedguy/beacon/internal/infra/mcp"
)

//go:generate go run . -docs -repo ../..

const (
	mdBegin = "<!-- BEGIN BEACON DOCS: %s -->"
	mdEnd   = "<!-- END BEACON DOCS: %s -->"
	jsBegin = "// BEGIN BEACON DOCS: %s"
	jsEnd   = "// END BEACON DOCS: %s"
)

// docFile names one file with generated marker blocks. The begin and end
// formats match the comment syntax of that file.
type docFile struct {
	path   string
	begin  string
	end    string
	blocks map[string]string
}

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
	docs := flag.Bool("docs", false, "rewrite catalog-derived doc marker blocks")
	repo := flag.String("repo", ".", "repository root used by -docs")
	flag.Parse()

	if !*docs && *out == "" {
		fmt.Fprintln(os.Stderr, "beacon-gen: -out or -docs is required")
		os.Exit(2)
	}
	if *docs {
		if err := generateDocs(*repo, mcp.ToolCatalog()); err != nil {
			fmt.Fprintf(os.Stderr, "beacon-gen: %v\n", err)
			os.Exit(1)
		}
	}
	if *out == "" {
		return
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

// docData is the catalog-derived content shared by the generated doc blocks.
type docData struct {
	all          []mcp.ToolSpec
	worker       []mcp.ToolSpec
	orchestrator []mcp.ToolSpec
	denied       string
	control      string
}

func buildDocData(specs []mcp.ToolSpec) docData {
	data := docData{all: append([]mcp.ToolSpec(nil), specs...)}
	for _, spec := range specs {
		if spec.OrchestratorOnly {
			data.orchestrator = append(data.orchestrator, spec)
		} else {
			data.worker = append(data.worker, spec)
		}
	}
	set := mcp.GeneratedPermissions(specs)
	denied := make([]string, 0, len(set.Global))
	for _, p := range set.Global {
		denied = append(denied, "`"+p.Action+"`")
	}
	sort.Strings(denied)
	data.denied = strings.Join(denied, ", ")
	for _, p := range set.Deny {
		if p.Effect == "allow" {
			data.control = "`" + p.Action + "`"
		}
	}
	return data
}

func roleOf(spec mcp.ToolSpec) string {
	if spec.OrchestratorOnly {
		return "orchestrator only"
	}
	return "worker"
}

func table(headers [3]string, rows []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "| %s | %s | %s |\n| --- | --- | --- |\n", headers[0], headers[1], headers[2])
	for _, row := range rows {
		b.WriteString(row)
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func agentsRows(specs []mcp.ToolSpec) []string {
	rows := make([]string, 0, len(specs))
	for _, spec := range specs {
		rows = append(rows, fmt.Sprintf("| `%s` | %s | %s |", spec.Name, spec.Description, roleOf(spec)))
	}
	return rows
}

func readmeRows(specs []mcp.ToolSpec) []string {
	rows := make([]string, 0, len(specs))
	for _, spec := range specs {
		rows = append(rows, fmt.Sprintf("| `%s` | %s | %s |", spec.Name, roleOf(spec), spec.Description))
	}
	return rows
}

func jsToolList(name string, specs []mcp.ToolSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "const %s = [\n", name)
	for _, spec := range specs {
		fmt.Fprintf(&b, "  %q,\n", spec.Name)
	}
	b.WriteString("]")
	return b.String()
}

func contextTools(data docData) string {
	return jsToolList("WORKER_TOOLS", data.worker) + "\n" + jsToolList("ORCHESTRATOR_TOOLS", data.orchestrator)
}

// generateDocs rewrites every named marker block from the tool catalog. Tool
// names, counts, descriptions and namespaced actions all come from one table.
func generateDocs(repo string, specs []mcp.ToolSpec) error {
	data := buildDocData(specs)
	agentsIntro := fmt.Sprintf(
		"`beacon mcp` serves %d tools over stdio: %d worker tools and %d orchestrator-only tools. `ToolCatalog()` in `internal/infra/mcp/registry.go` declares them.",
		len(data.all), len(data.worker), len(data.orchestrator))
	readmeIntro := fmt.Sprintf(
		"`beacon mcp` serves %d tools over stdio. `ToolCatalog()` in `internal/infra/mcp/registry.go` declares them and marks the orchestrator-only ones. %d tools handle runs and are available to every agent. %d tools manage task cards or force reconciliation and are denied globally except for `orchestrator` and `build`.",
		len(data.all), len(data.worker), len(data.orchestrator))

	files := []docFile{
		{
			path:  "AGENTS.md",
			begin: mdBegin,
			end:   mdEnd,
			blocks: map[string]string{
				"agents-tools":   agentsIntro + "\n\n" + table([3]string{"Tool", "Purpose", "Agent"}, agentsRows(data.all)),
				"denied-actions": data.denied,
				"control-action": data.control,
			},
		},
		{
			path:  "README.md",
			begin: mdBegin,
			end:   mdEnd,
			blocks: map[string]string{
				"readme-tools": readmeIntro + "\n\n" + table([3]string{"Tool", "Role", "What it does"}, readmeRows(data.all)),
			},
		},
		{
			path:  ".opencode/plugins/beacon-project/rules/context.js",
			begin: jsBegin,
			end:   jsEnd,
			blocks: map[string]string{
				"context-tools": contextTools(data),
			},
		},
	}
	for _, file := range files {
		path := filepath.Join(repo, file.path)
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", file.path, err)
		}
		updated := string(content)
		for name, body := range file.blocks {
			updated, err = replaceBlock(updated, name, file.begin, file.end, body)
			if err != nil {
				return fmt.Errorf("%s: %w", file.path, err)
			}
		}
		if updated == string(content) {
			continue
		}
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", file.path, err)
		}
	}
	return nil
}

// replaceBlock fills every block with the given name and leaves text outside
// the markers untouched. A missing begin or end marker is an error, so a stale
// or hand-edited doc cannot pass the sync test with an empty generation.
func replaceBlock(content, name, begin, end, body string) (string, error) {
	open := fmt.Sprintf(begin, name)
	close := fmt.Sprintf(end, name)
	var out strings.Builder
	rest := content
	found := false
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			break
		}
		tail := rest[i+len(open):]
		j := strings.Index(tail, close)
		if j < 0 {
			return "", fmt.Errorf("block %q has no end marker", name)
		}
		out.WriteString(rest[:i+len(open)])
		out.WriteString("\n")
		out.WriteString(body)
		out.WriteString("\n")
		out.WriteString(close)
		rest = tail[j+len(close):]
		found = true
	}
	if !found {
		return "", fmt.Errorf("block %q has no begin marker", name)
	}
	out.WriteString(rest)
	return out.String(), nil
}

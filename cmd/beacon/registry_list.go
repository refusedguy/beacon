package main

// T5.53: beacon registry list (ADR 0094).
//
// The command shows the project's executor registry: role, capabilities and
// configuration for every registered agent. It reads through a narrow,
// read-only store port scoped by project key, so it never registers, wakes or
// stops an executor. Dispatch registration in cmd/beacon/main.go is outside
// these OwnerFiles: this file owns the command and its output seam.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/refusedguy/beacon/internal/infra/store"
)

// registryListRunner is the dispatch seam, like the other command runners.
// CLI.dispatch routes `beacon registry list` here, and RegisterRegistryList
// replaces the runner in tests.
var registryListRunner = func(c *CLI, args []string) int { return runRegistryList(c, args) }

// RegisterRegistryList installs the registry list command implementation.
func RegisterRegistryList(fn func(*CLI, []string) int) { registryListRunner = fn }

// registryListEntry is the project-local view of one registry row. Capabilities
// are copied so a caller cannot mutate the store's slice through the result.
type registryListEntry struct {
	AgentID      string   `json:"agentId"`
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities"`
	ConfigID     string   `json:"configId"`
	Status       string   `json:"status"`
}

// registryListStore is the read-only infra port of the command. It lists the
// executors of one project and has no write or wake method.
type registryListStore interface {
	Executors(projectKey string) ([]store.ExecutorRecord, error)
}

// registryListDeps is the single port the command consumes.
type registryListDeps struct {
	Store registryListStore
}

type registryListDepsFactory func(*CLI) (registryListDeps, func() error, error)

var makeRegistryListDeps registryListDepsFactory = defaultRegistryListDeps

// SetRegistryListDeps replaces the dependency factory. Passing nil restores
// the production factory.
func SetRegistryListDeps(fn registryListDepsFactory) {
	if fn == nil {
		fn = defaultRegistryListDeps
	}
	makeRegistryListDeps = fn
}

// defaultRegistryListDeps opens the same state store the MCP process uses, so
// the CLI lists what beacon_watch_pr registered. The caller owns the closer.
func defaultRegistryListDeps(_ *CLI) (registryListDeps, func() error, error) {
	dir, err := stateDir()
	if err != nil {
		return registryListDeps{}, nil, err
	}
	db, err := openProcessStore(dir)
	if err != nil {
		return registryListDeps{}, nil, err
	}
	return registryListDeps{Store: db}, func() error { return db.Close() }, nil
}

// registryListEntries keeps only rows of projectKey and sorts them by agent id
// so both formats print the same stable order. An empty registry yields an
// empty, non-nil slice, never an error.
func registryListEntries(projectKey string, records []store.ExecutorRecord) []registryListEntry {
	out := make([]registryListEntry, 0, len(records))
	for _, r := range records {
		if r.ProjectKey != projectKey {
			continue
		}
		caps := append([]string(nil), r.Capabilities...)
		if caps == nil {
			caps = []string{}
		}
		out = append(out, registryListEntry{
			AgentID:      r.AgentID,
			Role:         r.Role,
			Capabilities: caps,
			ConfigID:     r.ConfigID,
			Status:       r.Status,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out
}

// renderRegistryList renders the same entries as a table or as JSON. An empty
// registry prints a header-only table or an empty JSON array, never an error.
func renderRegistryList(format string, entries []registryListEntry) (string, error) {
	switch format {
	case "json":
		if entries == nil {
			entries = []registryListEntry{}
		}
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetIndent("", "  ")
		if err := enc.Encode(entries); err != nil {
			return "", err
		}
		return b.String(), nil
	case "table":
		var b bytes.Buffer
		w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "AGENT\tROLE\tCAPS\tCONFIG\tSTATUS")
		for _, e := range entries {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				e.AgentID, e.Role, strings.Join(e.Capabilities, ","), e.ConfigID, e.Status)
		}
		if err := w.Flush(); err != nil {
			return "", err
		}
		return b.String(), nil
	default:
		return "", fmt.Errorf("registry list: неизвестный формат %q", format)
	}
}

// runRegistryList implements `beacon registry list [--format table|json]`. It
// reads the project's executors through the store port and renders them with
// the shared pure formatter, so table and JSON carry the same data.
func runRegistryList(c *CLI, args []string) int {
	fs := flag.NewFlagSet("registry list", flag.ContinueOnError)
	format := fs.String("format", "table", "формат вывода: table или json")
	if !parseFlags("registry list", args, c.errOut, fs) {
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(c.errOut, "beacon registry list: неизвестный формат %q, ожидается table или json\n", *format)
		writeUsage(c.errOut)
		return 2
	}

	deps, closeDeps, err := makeRegistryListDeps(c)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon registry list: %v\n", err)
		return 2
	}
	defer func() { _ = closeDeps() }()

	records, err := deps.Store.Executors(orchestratorProjectKey())
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon registry list: %v\n", err)
		return 2
	}
	rendered, err := renderRegistryList(*format, registryListEntries(orchestratorProjectKey(), records))
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon registry list: %v\n", err)
		return 2
	}
	fmt.Fprint(c.out, rendered)
	return 0
}

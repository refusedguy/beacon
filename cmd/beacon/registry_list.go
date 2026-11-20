package main

// T5.53: project-scoped registry listing (ADR 0094, ADR 0086).
//
// The list reads the current project's executor rows through a narrow,
// read-only port and renders agent id, role, capabilities and config id. An
// empty registry renders an empty table, not an error, so a project without
// registered executors is indistinguishable from a project whose registry
// file has just been created. Dispatch registration in cmd/beacon/main.go is
// outside these OwnerFiles: this file owns the list and its output seam, not
// the command switch (the same split T5.54 uses for registry check).

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/refusedguy/beacon/internal/infra/store"
)

// registryListSource is the read-only port of a registry listing. It exposes
// only the project-scoped read: no write, bind or session method can reach
// through it, so rendering a table cannot change the registry (ADR 0090).
type registryListSource interface {
	Executors(projectKey string) ([]store.ExecutorRecord, error)
}

// registryListEntry is one rendered row. Capabilities are copied and sorted
// so the output is stable regardless of the order the store decoded them in.
type registryListEntry struct {
	AgentID      string   `json:"agentID"`
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities"`
	ConfigID     string   `json:"configID"`
}

// registryListRows maps stored executors to renderable rows. The source is
// already scoped to one project key, so a row from another project cannot
// appear. An empty key means no project registry (global config): that renders
// as an empty list, never as a read error.
func registryListRows(source registryListSource, projectKey string) ([]registryListEntry, error) {
	rows := make([]registryListEntry, 0)
	if strings.TrimSpace(projectKey) == "" {
		return rows, nil
	}
	records, err := source.Executors(projectKey)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		caps := append([]string(nil), record.Capabilities...)
		sort.Strings(caps)
		rows = append(rows, registryListEntry{
			AgentID:      record.AgentID,
			Role:         record.Role,
			Capabilities: caps,
			ConfigID:     record.ConfigID,
		})
	}
	return rows, nil
}

// formatCapabilities joins capability names for the table column. Empty
// capabilities use a dash so the column stays scannable.
func formatCapabilities(caps []string) string {
	if len(caps) == 0 {
		return "-"
	}
	return strings.Join(caps, ",")
}

// writeRegistryListTable prints rows as a tab-aligned table. A registry with
// no rows still prints the header, so an empty registry prints an empty table
// rather than an error (card acceptance).
func writeRegistryListTable(out io.Writer, rows []registryListEntry) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT\tROLE\tCAPS\tCONFIG")
	for _, row := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			row.AgentID, row.Role, formatCapabilities(row.Capabilities), row.ConfigID)
	}
	return w.Flush()
}

// runRegistryList parses flags, reads the project registry and renders it.
// Both formats accept an empty registry and return code 0; only a bad flag or
// a failed read returns code 2.
func runRegistryList(args []string, out, errOut io.Writer, source registryListSource, projectKey string) int {
	fs := flag.NewFlagSet("registry list", flag.ContinueOnError)
	format := fs.String("format", "table", "формат вывода")
	if !parseFlags("registry list", args, errOut, fs) {
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(errOut, "beacon registry list: неизвестный формат %q, ожидается table или json\n", *format)
		return 2
	}
	rows, err := registryListRows(source, projectKey)
	if err != nil {
		fmt.Fprintf(errOut, "beacon registry list: %v\n", err)
		return 2
	}
	switch *format {
	case "json":
		if err := json.NewEncoder(out).Encode(rows); err != nil {
			fmt.Fprintf(errOut, "beacon registry list: %v\n", err)
			return 2
		}
	case "table":
		if err := writeRegistryListTable(out, rows); err != nil {
			fmt.Fprintf(errOut, "beacon registry list: %v\n", err)
			return 2
		}
	}
	return 0
}

// registryListRunner is the seam used by the dispatch registration outside
// these OwnerFiles. It resolves the project key, opens the shared state store
// and renders the list. The DB is opened only here; row mapping and rendering
// stay free of I/O setup.
func registryListRunner(args []string, out, errOut io.Writer) int {
	cfg, err := findRegistryConfig("")
	if err != nil {
		fmt.Fprintf(errOut, "beacon registry list: %v\n", err)
		return 2
	}
	dir, err := stateDir()
	if err != nil {
		fmt.Fprintf(errOut, "beacon registry list: %v\n", err)
		return 2
	}
	db, err := openProcessStore(dir)
	if err != nil {
		fmt.Fprintf(errOut, "beacon registry list: %v\n", err)
		return 2
	}
	defer db.Close()
	return runRegistryList(args, out, errOut, db, cfg.Key)
}

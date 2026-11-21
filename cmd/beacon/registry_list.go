package main

// T5.53: project registry list (ADR 0094, ADR 0086).
//
// The command prints the executors registered for one project with their role,
// capabilities and config id. The registry is read through a narrow port that
// is bound to a single project when it is built (store.NewScopedRegistry in the
// wiring layer, T5.29). The port has no project argument and no write method,
// so this command cannot widen its own scope. The renderer is pure, so both
// formats and the empty-registry case are tested without a live daemon.
// Dispatch registration in cmd/beacon/main.go is outside these OwnerFiles.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
)

// registryListEntry is one registry row reduced to what the command prints.
// project is internal: it records the source project so a test can prove the
// reader's scope. It is never printed or serialized.
type registryListEntry struct {
	Project      string
	ID           string
	Role         string
	Capabilities []string
	Config       string
}

// registryListReader is the read-only source of one project's registry. A
// reader is bound to exactly one project at construction and exposes no way to
// name another one, so the output of this command is limited to that project.
type registryListReader interface {
	Executors() ([]registryListEntry, error)
}

// registryListSource is the wiring seam. The wiring layer installs a reader
// bound to the current project. The default refuses rather than returning an
// unscoped reader that could list every project.
var registryListSource = func() (registryListReader, error) {
	return nil, fmt.Errorf("registry reader not connected")
}

// registryListRunner lets the wiring layer install the command without this
// file touching cmd/beacon/main.go.
var registryListRunner = func(c *CLI, args []string) int { return runRegistryList(c, args) }

// registryListRow is the printed projection of one entry.
type registryListRow struct {
	ID           string   `json:"id"`
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities"`
	Config       string   `json:"config"`
}

// registryListRows projects entries to printable rows. It copies the
// capability slice so a later change to an entry cannot change printed rows,
// sorts capabilities for stable output and orders rows by agent id.
func registryListRows(entries []registryListEntry) []registryListRow {
	rows := make([]registryListRow, 0, len(entries))
	for _, e := range entries {
		caps := append([]string(nil), e.Capabilities...)
		sort.Strings(caps)
		rows = append(rows, registryListRow{
			ID:           e.ID,
			Role:         e.Role,
			Capabilities: caps,
			Config:       e.Config,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

// renderRegistryTable writes one line per row. An empty slice writes only the
// header, so an empty registry prints an empty table instead of an error.
func renderRegistryTable(w io.Writer, rows []registryListRow) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tROLE\tCAPABILITIES\tCONFIG")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			dash(r.ID), dash(r.Role), strings.Join(r.Capabilities, ","), dash(r.Config))
	}
	_ = tw.Flush()
}

// renderRegistryJSON encodes a non-nil slice, so an empty registry is [] and
// never null.
func renderRegistryJSON(w io.Writer, rows []registryListRow) error {
	return json.NewEncoder(w).Encode(rows)
}

func renderRegistry(format string, rows []registryListRow, w io.Writer) error {
	switch format {
	case "table":
		renderRegistryTable(w, rows)
		return nil
	case "json":
		return renderRegistryJSON(w, rows)
	default:
		return fmt.Errorf("неверное значение --format %q, ожидается table или json", format)
	}
}

// runRegistryList implements `beacon registry list [--format table|json]`.
func runRegistryList(c *CLI, args []string) int {
	fs := flag.NewFlagSet("registry list", flag.ContinueOnError)
	format := fs.String("format", "table", "формат вывода: table или json")
	if !parseFlags("registry list", args, c.errOut, fs) {
		return 2
	}
	reader, err := registryListSource()
	if err != nil {
		fmt.Fprintf(c.errOut, "%v\n", cliError("registry list", err))
		return 2
	}
	entries, err := reader.Executors()
	if err != nil {
		fmt.Fprintf(c.errOut, "%v\n", cliError("registry list", err))
		return 2
	}
	if err := renderRegistry(*format, registryListRows(entries), c.out); err != nil {
		fmt.Fprintf(c.errOut, "%v\n", cliError("registry list", err))
		return 2
	}
	return 0
}

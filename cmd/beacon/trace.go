package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/infra/store"
)

// The trace command is registered here rather than in catalog.go or main.go so
// this card owns the whole command. init order puts main.go before trace.go,
// so runnerFor already exists when these entries are added.
func init() {
	catalog = append(catalog, command{
		Name:    "trace",
		Summary: "Показать журнал wake, ack и retry одной сессии за интервал",
		Usage:   "beacon trace --session <id> [--since <duration>] [--format table|json]",
	})
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Name < catalog[j].Name })
	commandFormats["trace"] = []string{"table", "json"}
	if runnerFor == nil {
		runnerFor = make(map[string]commandRunner)
	}
	runnerFor["trace"] = func(c *CLI, args []string) int { return traceRunner(c, args) }
}

// traceRunner is the dispatch seam. RegisterTrace replaces it in tests.
var traceRunner = func(c *CLI, args []string) int { return runTrace(c, args) }

// RegisterTrace installs the trace command implementation.
func RegisterTrace(fn func(*CLI, []string) int) { traceRunner = fn }

// traceNow is the clock used by --since. Tests pin it to a fixed instant.
var traceNow = time.Now

// traceEventStore is the only store capability trace needs. *store.DB satisfies
// it, and tests supply an in-memory reader.
type traceEventStore interface {
	LoadEvents() ([]store.JournalEvent, []store.LoadError, error)
}

type traceDeps struct {
	Events traceEventStore
}

type traceDepsFactory func(*CLI) (traceDeps, func() error, error)

var makeTraceDeps traceDepsFactory = defaultTraceDeps

func defaultTraceDeps(_ *CLI) (traceDeps, func() error, error) {
	dir, err := stateDir()
	if err != nil {
		return traceDeps{}, nil, err
	}
	db, err := openProcessStore(dir)
	if err != nil {
		return traceDeps{}, nil, err
	}
	return traceDeps{Events: db}, func() error { return db.Close() }, nil
}

// traceRecord is one printed line. Every field comes from a stored event; an
// absent reason stays empty instead of being guessed from the message.
type traceRecord struct {
	At       string `json:"at"`
	WatchID  string `json:"watchID"`
	Kind     string `json:"kind"`
	Revision int64  `json:"revision"`
	Reason   string `json:"reason"`
	Source   string `json:"source"`
}

type tracePayload struct {
	Kind     string `json:"kind"`
	Message  string `json:"message"`
	Reason   string `json:"reason"`
	Source   string `json:"source"`
	Revision int64  `json:"revision"`
	WatchID  string `json:"watchID"`
}

var traceKinds = map[string]bool{"wake": true, "ack": true, "retry": true}

func traceKind(payload tracePayload) string {
	kind := strings.TrimSpace(payload.Kind)
	if kind == "wake-intent" {
		return "wake"
	}
	return kind
}

// collectTrace selects one session's wake, ack and retry rows inside the
// window. Malformed payloads are skipped: a row that cannot be decoded is not
// evidence, and its message is never used as a reason.
func collectTrace(events []store.JournalEvent, session string, since time.Time) []traceRecord {
	out := make([]traceRecord, 0, len(events))
	for _, ev := range events {
		if ev.WatchID != session {
			continue
		}
		at := time.Unix(0, ev.CreatedAt).UTC()
		if at.Before(since) {
			continue
		}
		var payload tracePayload
		if len(ev.Payload) > 0 {
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				continue
			}
		}
		kind := traceKind(payload)
		if !traceKinds[kind] {
			continue
		}
		out = append(out, traceRecord{
			At:       at.Format(time.RFC3339Nano),
			WatchID:  ev.WatchID,
			Kind:     kind,
			Revision: payload.Revision,
			Reason:   payload.Reason,
			Source:   payload.Source,
		})
	}
	return out
}

func writeTraceTable(w io.Writer, records []traceRecord) {
	for _, r := range records {
		fmt.Fprintf(w, "at=%s watchID=%s kind=%s revision=%d reason=%q source=%q\n",
			r.At, r.WatchID, r.Kind, r.Revision, r.Reason, r.Source)
	}
}

// runTrace implements `beacon trace --session <id> [--since <duration>]`.
func runTrace(c *CLI, args []string) int {
	fs := flag.NewFlagSet("trace", flag.ContinueOnError)
	session := fs.String("session", "", "идентификатор сессии")
	since := fs.String("since", "10m", "интервал, например 10m")
	format := fs.String("format", "table", "формат вывода: table или json")
	jsonAlias := fs.Bool("json", false, "псевдоним --format=json")
	if !parseFlags("trace", args, c.errOut, fs) {
		return 2
	}
	if *jsonAlias {
		*format = "json"
	}
	if !parseCommandFormat("trace", *format, c.errOut, false) {
		return 2
	}
	if strings.TrimSpace(*session) == "" {
		fmt.Fprintf(c.errOut, "beacon trace: требуется --session\n")
		return 2
	}
	window, err := time.ParseDuration(*since)
	if err != nil || window <= 0 {
		fmt.Fprintf(c.errOut, "beacon trace: неверный --since %q: ожидается положительная длительность\n", *since)
		return 2
	}

	deps, closeDeps, err := makeTraceDeps(c)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon trace: %v\n", err)
		return 2
	}
	defer func() { _ = closeDeps() }()

	events, _, err := deps.Events.LoadEvents()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon trace: %v\n", err)
		return 2
	}
	records := collectTrace(events, *session, traceNow().Add(-window))
	if len(records) == 0 {
		fmt.Fprintln(c.out, "no evidence")
		return 0
	}
	switch *format {
	case "json":
		if err := json.NewEncoder(c.out).Encode(records); err != nil {
			fmt.Fprintf(c.errOut, "beacon trace: %v\n", err)
			return 2
		}
	default:
		writeTraceTable(c.out, records)
	}
	return 0
}

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/refusedguy/beacon/internal/infra/mcp"
)

// watchSnapshot is the read-only Beacon port used by beacon watch. It has the
// same shape as mcp.SnapshotReader, so production wiring can reuse the MCP
// snapshot port while tests replace the whole function (T3.12).
//
// The default opens the real state store, takes one snapshot and closes the
// store again. beacon watch is deliberately single-shot: one snapshot, one
// session.list call, one rendered table. There is no event loop here.
var watchSnapshot = func() (mcp.StateSnapshot, error) {
	deps, closeStore, err := openMCPDependencies()
	if err != nil {
		return mcp.StateSnapshot{}, err
	}
	defer func() { _ = closeStore() }()
	return deps.Snapshot.Snapshot()
}

// watchRunner holds the command implementation so the CLI dispatch can call
// it without depending on the rendering details. RegisterWatch replaces it in
// tests, mirroring RegisterTask and RegisterMCP in main.go and mcp.go.
var watchRunner = func(c *CLI, args []string) int { return runWatch(c, args) }

// RegisterWatch installs the watch command implementation.
func RegisterWatch(fn func(*CLI, []string) int) {
	watchRunner = fn
}

// opencodeSession is the subset of a session.list row the table needs. Status
// is kept raw because v2 reports it either as a string or as {"type": "..."}.
type opencodeSession struct {
	ID     string          `json:"id"`
	Agent  string          `json:"agent"`
	Status json.RawMessage `json:"status"`
}

// state returns the ephemeral OpenCode state: idle, retry or busy. It is never
// used as the task's readiness, only as an informational column (ADR 0029).
func (s opencodeSession) state() string {
	if len(s.Status) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(s.Status, &text) == nil {
		return text
	}
	var obj struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(s.Status, &obj) == nil {
		return obj.Type
	}
	return ""
}

// watchRow is one rendered line of the watch table. Verdict comes from Beacon
// and is the readiness signal. OpenCodeState is the raw session state and does
// not decide whether the task is done (ADR 0029).
type watchRow struct {
	Watch         string `json:"watch"`
	Verdict       string `json:"verdict"`
	Revision      uint64 `json:"revision"`
	Subscriber    string `json:"subscriberSession,omitempty"`
	Agent         string `json:"agent,omitempty"`
	OpenCodeState string `json:"opencodeState,omitempty"`
}

// parseSessions reads the output of `opencode api session.list`. The CLI wraps
// the payload in {"data": [...]}; a bare array is accepted as well so a mock
// or a future CLI version works unchanged.
func parseSessions(raw []byte) ([]opencodeSession, error) {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Data) != 0 {
		var sessions []opencodeSession
		if err := json.Unmarshal(envelope.Data, &sessions); err == nil {
			return sessions, nil
		}
	}
	var sessions []opencodeSession
	if err := json.Unmarshal(raw, &sessions); err != nil {
		return nil, fmt.Errorf("session.list: %w", err)
	}
	return sessions, nil
}

// joinWatches pairs every subscriber with the session whose id equals the
// subscriber target. The target is the opaque sessionID carried in the tool
// request's _meta "ai.opencode/sessionID" (internal/infra/mcp/meta.go:8).
// An unmatched target still produces a row so the watch is never hidden; only
// the agent and state columns stay empty in that case.
func joinWatches(snap mcp.StateSnapshot, sessions []opencodeSession) []watchRow {
	byID := make(map[string]opencodeSession, len(sessions))
	for _, s := range sessions {
		byID[s.ID] = s
	}

	rows := make([]watchRow, 0)
	for _, w := range snap.ActiveWatches {
		// A watch with no subscriber is still visible: readiness comes from
		// the Beacon verdict alone, not from a live OpenCode session.
		if len(w.Subscribers) == 0 {
			rows = append(rows, watchRow{
				Watch:    w.ID,
				Verdict:  string(w.Verdict),
				Revision: w.Revision,
			})
			continue
		}
		for _, sub := range w.Subscribers {
			row := watchRow{
				Watch:      w.ID,
				Verdict:    string(w.Verdict),
				Revision:   w.Revision,
				Subscriber: sub.Target,
			}
			if session, ok := byID[sub.Target]; ok {
				row.Agent = session.Agent
				row.OpenCodeState = session.state()
			}
			rows = append(rows, row)
		}
	}
	return rows
}

func dash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func renderWatchTable(w io.Writer, rows []watchRow) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "WATCH\tVERDICT\tREVISION\tSUBSCRIBER SESSION\tAGENT\tOPENCODE STATE")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n",
			r.Watch, r.Verdict, r.Revision,
			dash(r.Subscriber), dash(r.Agent), dash(r.OpenCodeState))
	}
	_ = tw.Flush()
}

func renderWatchJSON(w io.Writer, rows []watchRow) error {
	return json.NewEncoder(w).Encode(rows)
}

// runWatch is the beacon watch command. It takes exactly one Beacon snapshot
// and one `opencode api session.list` call, joins subscribers to sessions and
// prints the result. The --interval flag is accepted for compatibility with
// the documented CLI shape but does not start a loop (T3.12: one snapshot).
func runWatch(c *CLI, args []string) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	interval := fs.Duration("interval", 2*time.Second, "интервал обновления (зарезервировано, один снимок)")
	format := fs.String("format", "table", "формат вывода: table или json")
	if !parseFlags("watch", args, c.errOut, fs) {
		return 2
	}
	_ = interval
	if *format != "table" && *format != "json" {
		fmt.Fprintf(c.errOut, "beacon watch: неизвестный формат %q, ожидается table или json\n", *format)
		writeUsage(c.errOut)
		return 2
	}

	snap, err := watchSnapshot()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon watch: snapshot: %v\n", err)
		return 2
	}

	res := c.run(c.root, "opencode", []string{"api", "session.list"}, nil)
	if res.code != 0 {
		reason := strings.TrimSpace(string(res.stderr))
		if reason == "" {
			reason = strings.TrimSpace(string(res.stdout))
		}
		fmt.Fprintf(c.errOut, "beacon watch: opencode api session.list: %s\n", reason)
		return res.code
	}

	sessions, err := parseSessions(res.stdout)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon watch: %v\n", err)
		return 2
	}

	rows := joinWatches(snap, sessions)
	if *format == "json" {
		if err := renderWatchJSON(c.out, rows); err != nil {
			fmt.Fprintf(c.errOut, "beacon watch: %v\n", err)
			return 2
		}
		return 0
	}
	renderWatchTable(c.out, rows)
	return 0
}

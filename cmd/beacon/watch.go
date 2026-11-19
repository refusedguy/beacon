package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"syscall"
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

// ---------------------------------------------------------------------------
// T3.114 / ADR 0048 p4: beacon gc.
//
// The command clears stale records left behind by leases and the queue: leases
// whose TTL elapsed, records whose process is dead, and tickets whose deadline
// passed. Deletion is an explicit, separate action: a plain run only lists what
// would go, --apply removes it and reports what was removed. The command never
// hands a worktree to a new owner (T3.65 reclaim) and never changes the lease
// TTL of T3.97. It deletes stale records, nothing else.
// ---------------------------------------------------------------------------

// gcKind is the class of a stale object.
type gcKind string

const (
	gcKindLease  gcKind = "lease"
	gcKindPID    gcKind = "pid"
	gcKindTicket gcKind = "ticket"

	gcReasonExpired = "expired"
	gcReasonDead    = "dead"
	gcReasonStale   = "stale"
)

// gcItem is one object the collector found and may remove.
type gcItem struct {
	Kind   gcKind `json:"kind"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// The raw state is read from the stores that own each record type. watch.go
// fixes only the shape, the scan/apply split and the report; it does not reach
// into a concrete store. A zero ExpiresAt means the ticket has no deadline.
type gcLease struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type gcPID struct {
	ID  string `json:"id"`
	PID int    `json:"pid"`
}

type gcTicket struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type gcState struct {
	Leases  []gcLease  `json:"leases"`
	PIDs    []gcPID    `json:"pids"`
	Tickets []gcTicket `json:"tickets"`
}

// gcRead loads the raw records and gcWrite removes exactly the named ones.
// Both are replaceable so the command can be tested without a live store.
type gcRead func() (gcState, error)
type gcWrite func(items []gcItem) ([]gcItem, error)

var (
	gcNow              = time.Now
	gcReadState gcRead = func() (gcState, error) {
		return gcState{}, errors.New("gc: хранилище аренд и талонов не подключено")
	}
	gcWriteState gcWrite = func([]gcItem) ([]gcItem, error) {
		return nil, errors.New("gc: хранилище аренд и талонов не подключено")
	}
	// gcProcessAlive mirrors the signal-0 check of taskstore and tool_list_running:
	// EPERM still means the process exists, only another user owns it.
	gcProcessAlive = func(pid int) bool {
		if pid <= 0 {
			return false
		}
		err := syscall.Kill(pid, 0)
		return err == nil || errors.Is(err, syscall.EPERM)
	}
	gcRunner = func(c *CLI, args []string) int { return runGC(c, args) }
)

// RegisterGC installs the gc command implementation, mirroring RegisterWatch.
func RegisterGC(fn func(*CLI, []string) int) { gcRunner = fn }

// RegisterGCStores installs the read and write halves of the collector store.
// The stores own the records; this command only fixes the contract. A nil half
// keeps the previous implementation.
func RegisterGCStores(read gcRead, write gcWrite) {
	if read != nil {
		gcReadState = read
	}
	if write != nil {
		gcWriteState = write
	}
}

// gcStale selects what the collector may remove. A lease is stale once its
// recorded expiry passed, a PID record once its process is gone, and a ticket
// once its deadline passed. The check uses the recorded expiry, not a new TTL,
// and never looks at who owns a worktree: reclamation and TTL renewal are other
// mechanisms (T3.65 and T3.97).
func gcStale(state gcState, now time.Time) []gcItem {
	items := make([]gcItem, 0)
	for _, l := range state.Leases {
		if !now.Before(l.ExpiresAt) {
			items = append(items, gcItem{Kind: gcKindLease, ID: l.ID, Reason: gcReasonExpired})
		}
	}
	for _, p := range state.PIDs {
		if !gcProcessAlive(p.PID) {
			items = append(items, gcItem{Kind: gcKindPID, ID: p.ID, Reason: gcReasonDead})
		}
	}
	for _, t := range state.Tickets {
		if !t.ExpiresAt.IsZero() && !now.Before(t.ExpiresAt) {
			items = append(items, gcItem{Kind: gcKindTicket, ID: t.ID, Reason: gcReasonStale})
		}
	}
	return items
}

func renderGCTable(w io.Writer, items []gcItem, applied bool) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tID\tREASON")
	for _, it := range items {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", it.Kind, it.ID, it.Reason)
	}
	_ = tw.Flush()
	if applied {
		fmt.Fprintf(w, "УДАЛЕНО: %d\n", len(items))
		return
	}
	fmt.Fprintf(w, "НАЙДЕНО: %d (просмотр без удаления, --apply удалит)\n", len(items))
}

// gcResult is the JSON form of both a dry run and an apply. Items holds the
// plan for a dry run and the actually removed records for an apply.
type gcResult struct {
	Mode    string   `json:"mode"`
	Removed int      `json:"removed"`
	Items   []gcItem `json:"items"`
}

func renderGCJSON(w io.Writer, result gcResult) error {
	return json.NewEncoder(w).Encode(result)
}

// runGC is the beacon gc command. Without --apply it is read-only; with --apply
// it removes exactly the objects found by the scan and reports the count.
func runGC(c *CLI, args []string) int {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	format := fs.String("format", "table", "формат вывода: table или json")
	apply := fs.Bool("apply", false, "удалить найденные записи (по умолчанию только просмотр)")
	if !parseFlags("gc", args, c.errOut, fs) {
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(c.errOut, "beacon gc: неизвестный формат %q, ожидается table или json\n", *format)
		writeUsage(c.errOut)
		return 2
	}

	state, err := gcReadState()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon gc: чтение состояния: %v\n", err)
		return 2
	}
	plan := gcStale(state, gcNow())

	if *format == "json" && !*apply {
		if err := renderGCJSON(c.out, gcResult{Mode: "dry-run", Items: plan}); err != nil {
			fmt.Fprintf(c.errOut, "beacon gc: %v\n", err)
			return 2
		}
		return 0
	}
	if !*apply {
		renderGCTable(c.out, plan, false)
		return 0
	}

	removed, err := gcWriteState(plan)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon gc: удаление: %v\n", err)
		return 2
	}
	if removed == nil {
		removed = []gcItem{}
	}
	if *format == "json" {
		if err := renderGCJSON(c.out, gcResult{Mode: "applied", Removed: len(removed), Items: removed}); err != nil {
			fmt.Fprintf(c.errOut, "beacon gc: %v\n", err)
			return 2
		}
		return 0
	}
	renderGCTable(c.out, removed, true)
	return 0
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/refusedguy/beacon/internal/infra/cibridge"
	"github.com/refusedguy/beacon/internal/infra/httpapi"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/observe"
	"github.com/refusedguy/beacon/internal/infra/opencode"
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

// watchSessionSource reads the session list through the one HTTP port. No
// child process is spawned and no fallback server starter is installed: a
// missing service registration is an honest error with exit code 2. The exit
// code stays in the result so tests can still observe the command's status.
var watchSessionSource = func() ([]opencode.Session, int, error) {
	port, err := newReadOnlyOpencodePort()
	if err != nil || port == nil {
		if err == nil {
			err = errors.New("opencode service registration missing")
		}
		return nil, 2, err
	}
	sessions, err := port.Sessions(context.Background())
	if err != nil {
		return nil, 2, err
	}
	return sessions, 0, nil
}

// sessionsToWatch keeps only the session fields the table renders. The wire
// payload is decoded once by opencode.ParseSessions inside the HTTP client.
func sessionsToWatch(sessions []opencode.Session) []opencodeSession {
	out := make([]opencodeSession, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, opencodeSession{ID: s.ID, Agent: s.Agent, Status: s.Status})
	}
	return out
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
	// Role, Capabilities and Config are the ADR 0094 JOIN axes. They come
	// from the read-only registry projection, never from a write path.
	// Empty means the subscriber has no registry row; the live table marks
	// those cells with a dash (T5.58).
	Role         string   `json:"role,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Config       string   `json:"config,omitempty"`
	// Attention is the ADR 0058 п6 column. "+" marks the row the manager
	// should look at now; "-" means no signal. It is advisory only and never
	// changes the Beacon verdict (ADR 0017 and ADR 0026).
	Attention string `json:"attention,omitempty"`
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
	// The registry is keyed by the opaque executor id, which is the same
	// sessionID carried by the subscriber target in _meta (ADR 0086/0094).
	// A snapshot without registry entries keeps the previous row shape.
	byRegistry := make(map[string]mcp.RegistryEntry, len(snap.Registry))
	for _, entry := range snap.Registry {
		byRegistry[entry.ID] = entry
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
			if entry, ok := byRegistry[sub.Target]; ok {
				row.Role = string(entry.Role)
				row.Config = entry.Config
				caps := make([]string, 0, len(entry.Capabilities))
				for _, cap := range entry.Capabilities {
					caps = append(caps, string(cap))
				}
				row.Capabilities = caps
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

// renderLiveTable is the ADR 0060 JOIN table: one line per subscriber with the
// ADR 0060 columns followed by the ADR 0094 registry axes ROLE, CAPS and CONFIG
// (T5.58). The header says SESSION rather than SUBSCRIBER SESSION to match the
// ADR. No TUI is involved.
func renderLiveTable(w io.Writer, rows []watchRow) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "WATCH\tVERDICT\tREVISION\tSESSION\tAGENT\tOPENCODE STATE\tATTENTION\tROLE\tCAPS\tCONFIG")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Watch, r.Verdict, r.Revision,
			dash(r.Subscriber), dash(r.Agent), dash(r.OpenCodeState), dash(r.Attention),
			dash(r.Role), dash(strings.Join(r.Capabilities, ",")), dash(r.Config))
	}
	_ = tw.Flush()
}

// watchIntervalEnv overrides the live re-read interval when --interval is
// absent.
const watchIntervalEnv = "BEACON_WATCH_INTERVAL"

// watchIntervalDefault is both the --interval default and the value used when
// the env value is invalid. A typo cannot stop the loop.
const watchIntervalDefault = 2 * time.Second

// watchIntervalFromEnv reads BEACON_WATCH_INTERVAL. An empty value leaves the
// flag default in place. A bad value is reported and ignored.
func watchIntervalFromEnv() (time.Duration, bool) {
	d, ok, raw := cibridge.DurationFromEnv(watchIntervalEnv, 0)
	if !ok && raw != "" {
		observe.WarnConfig(watchIntervalEnv, raw, "ожидается положительная длительность, используется значение по умолчанию")
	}
	return d, ok
}

// watchLiveDeps collects the seams of the HTTP live loop. A caller can pass
// them to runWatch explicitly instead of replacing package globals. The globals
// remain the defaults for callers that still use the old substitution seam.
type watchLiveDeps struct {
	stateDir       func() (string, error)
	client         func(httpapi.ClientConn) (watchHTTPPort, error)
	lifetime       func() (context.Context, context.CancelFunc)
	reconnectDelay time.Duration
}

func defaultWatchLiveDeps() watchLiveDeps {
	return watchLiveDeps{
		stateDir:       watchHTTPStateDir,
		client:         watchHTTPClient,
		lifetime:       watchLiveContext,
		reconnectDelay: watchReconnectDelay,
	}
}

// runWatch is the beacon watch command. Without --live it takes exactly one
// Beacon snapshot and one `opencode api session.list` call (T3.12). With --live
// it becomes a long-running, event-driven loop over the SSE subscription
// (T3.141 / ADR 0060). The optional deps argument lets a test pass the live
// seams explicitly; ordinary callers keep the two-argument form.
func runWatch(c *CLI, args []string, liveDeps ...watchLiveDeps) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	interval := fs.Duration("interval", watchIntervalDefault, "период предохранительного перечитывания снимка в живом контуре")
	live := fs.Bool("live", false, "жить по событиям SSE /api/event вместо одного снимка")
	format := fs.String("format", "table", "формат вывода: table или json")
	wake := fs.Bool("wake", false, "батч событий за 200мс в одно пробуждение сессии")
	if !parseFlags("watch", args, c.errOut, fs) {
		return 2
	}
	// An explicit --interval wins over the env override.
	intervalExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "interval" {
			intervalExplicit = true
		}
	})
	if !intervalExplicit {
		if fromEnv, ok := watchIntervalFromEnv(); ok {
			*interval = fromEnv
		}
	}
	if !parseFormatFlag(*format, "beacon watch: неизвестный формат %q, ожидается table или json\n", c.errOut, true) {
		return 2
	}
	if *live {
		if len(liveDeps) > 0 {
			return runWatchLiveHTTPWithDeps(c, *interval, *format, *wake, liveDeps[0])
		}
		return watchLiveRunner(c, *interval, *format, *wake)
	}

	snap, err := watchSnapshot()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon watch: snapshot: %v\n", err)
		return 2
	}

	rawSessions, exitCode, err := watchSessionSource()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon watch: session list: %v\n", err)
		return exitCode
	}

	sessions := sessionsToWatch(rawSessions)

	rows := joinWatches(snap, sessions)
	if *wake {
		wakeOut := c.out
		if *format == "json" {
			wakeOut = c.errOut
		}
		send := func(target, text string) error {
			fmt.Fprintf(wakeOut, "wake session=%s %s\n", target, text)
			return nil
		}
		if err := runWatchBatchedWake(rows, time.Now(), send); err != nil {
			fmt.Fprintf(c.errOut, "beacon watch: wake: %v\n", err)
			return 2
		}
	}
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
// T3.141 / ADR 0060 p2: beacon watch --live.
//
// The command is a presentation layer. It owns no HTTP client and no SSE
// parser: T3.144 supplies the opencode client and T3.145 wires it into
// dispatch. This card fixes only the narrow port the loop consumes, so the two
// branches can merge without a conflict.
// ---------------------------------------------------------------------------

// liveEvent is one notification delivered by the SSE subscription. The body
// stays opaque: on every event the loop re-reads Beacon and the session list
// instead of interpreting the event payload (ADR 0060 p2).
type liveEvent struct {
	Type string `json:"type"`
	Data []byte `json:"data,omitempty"`
}

// liveSource is the whole live dependency of beacon watch: a session list and
// an SSE event stream. Events must yield only events received after the call;
// closing the channel means the SSE connection dropped and the loop must
// resubscribe by calling Events again. The stream carries no history (ADR 0025).
type liveSource interface {
	SessionList() ([]opencodeSession, error)
	Events(ctx context.Context) <-chan liveEvent
}

// watchLiveSource is the installed live source. It is nil until dispatch wires
// a real client (T3.145), so --live fails closed instead of reaching for a
// transport this card must not create.
var watchLiveSource liveSource

// SetLiveSource installs the source used by beacon watch --live. Passing nil
// clears it. Tests use this to substitute a stand-in; dispatch uses it after
// the opencode client lands.
func SetLiveSource(src liveSource) {
	watchLiveSource = src
}

// watchLiveContext returns the lifetime of the live loop. Production stops on
// SIGINT or SIGTERM; tests replace it with a cancellable context.
var watchLiveContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// watchReconnectDelay keeps a source that immediately closes its channel from
// spinning the loop. It is short enough to be invisible in tests and is
// interruptible by the lifetime context.
var watchReconnectDelay = 50 * time.Millisecond

// eventTailMax bounds the replay lines carried in one live snapshot. The board
// keeps a short tail too, so both screens show a comparable amount of history.
const eventTailMax = 12

// markAttention fills the ADR 0058 п6 column. A row asks for attention when its
// Beacon verdict is needs_input (a task parked for a human) or when an open
// attention request names its agent or its watch. The earliest request wins and
// ties break by agent then task, so the result is stable across reads. Nothing
// here touches readiness: the verdict still comes from Beacon.
func markAttention(rows []watchRow, requests []attentionRequest) {
	byAgent := make(map[string]attentionRequest)
	byTask := make(map[string]attentionRequest)
	for _, req := range requests {
		if agent := strings.TrimSpace(req.Agent); agent != "" && earlierAttention(req, byAgent[agent]) {
			byAgent[agent] = req
		}
		if task := strings.TrimSpace(req.TaskID); task != "" && earlierAttention(req, byTask[task]) {
			byTask[task] = req
		}
	}
	for i := range rows {
		r := &rows[i]
		if r.Verdict == verdictNeedsInput {
			r.Attention = attentionYes
			continue
		}
		var req attentionRequest
		var ok bool
		if r.Agent != "" {
			req, ok = byAgent[r.Agent]
		}
		if !ok && r.Watch != "" {
			req, ok = byTask[r.Watch]
		}
		if ok && (req.Agent != "" || req.TaskID != "") {
			r.Attention = attentionYes
		}
	}
}

// earlierAttention reports whether candidate should replace current. A zero
// current means the key is new, so any real request wins.
func earlierAttention(candidate, current attentionRequest) bool {
	if current.RequestedAt.IsZero() {
		return true
	}
	if !candidate.RequestedAt.Equal(current.RequestedAt) {
		return candidate.RequestedAt.Before(current.RequestedAt)
	}
	if candidate.Agent != current.Agent {
		return candidate.Agent < current.Agent
	}
	return candidate.TaskID < current.TaskID
}

// verdictNeedsInput is the Beacon verdict for a task parked for a human. It is
// a string here because cmd/beacon must not import internal/domain (layers gate).
const verdictNeedsInput = "needs_input"

const attentionYes = "+"

// watchRender is one refresh: a fresh Beacon snapshot, a fresh session list,
// the JOIN and one printed table. Every event, reconnect and interval tick
// calls it, so the table always reflects current state rather than event data.
func watchRender(c *CLI, src liveSource, format string, wake bool) int {
	snap, err := watchSnapshot()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon watch: snapshot: %v\n", err)
		return 2
	}
	sessions, err := src.SessionList()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon watch: session.list: %v\n", err)
		return 2
	}
	rows := joinWatches(snap, sessions)
	// Attention is advisory. A missing or broken request file must not blank
	// the whole table, so the error is dropped here.
	if requests, err := readAttentionRequests(); err == nil {
		markAttention(rows, requests)
	}
	if wake {
		wakeOut := c.out
		if format == "json" {
			wakeOut = c.errOut
		}
		send := func(target, text string) error {
			fmt.Fprintf(wakeOut, "wake session=%s %s\n", target, text)
			return nil
		}
		if err := runWatchBatchedWake(rows, time.Now(), send); err != nil {
			fmt.Fprintf(c.errOut, "beacon watch: wake: %v\n", err)
			return 2
		}
	}
	if format == "json" {
		if err := renderWatchJSON(c.out, rows); err != nil {
			fmt.Fprintf(c.errOut, "beacon watch: %v\n", err)
			return 2
		}
		return 0
	}
	renderLiveTable(c.out, rows)
	return 0
}

// runWatchLive is the event-driven loop. Each subscription starts with a fresh
// snapshot; each event and each interval tick re-reads state and prints. When
// the event channel closes, the loop waits briefly, resubscribes and re-reads
// the snapshot without replaying missed events (ADR 0025). ctx cancels the
// whole loop for graceful shutdown.
func runWatchLive(c *CLI, interval time.Duration, format string, wake bool) int {
	src := watchLiveSource
	if src == nil {
		fmt.Fprintf(c.errOut, "beacon watch: --live: источник событий не подключён\n")
		return 2
	}
	ctx, cancel := watchLiveContext()
	defer cancel()

	var ticks <-chan time.Time
	if interval > 0 {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		ticks = ticker.C
	}

	for {
		if ctx.Err() != nil {
			return 0
		}
		events := src.Events(ctx)
		// (Re)connect always starts from a fresh snapshot. The SSE stream has
		// no history, so dropped events are never replayed (ADR 0025).
		if code := watchRender(c, src, format, wake); code != 0 {
			return code
		}
		disconnected := false
		for !disconnected {
			select {
			case <-ctx.Done():
				return 0
			case _, ok := <-events:
				if !ok {
					disconnected = true
					continue
				}
				// A later refresh failure is a warning on stderr, not a
				// reason to kill a long-lived watch.
				_ = watchRender(c, src, format, wake)
			case <-ticks:
				_ = watchRender(c, src, format, wake)
			}
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(watchReconnectDelay):
		}
	}
}

// ---------------------------------------------------------------------------
// W8-B: beacon watch --live reads Beacon's own HTTP projection.
//
// The old SSE loop above stays as a legacy path for tests of the OpenCode
// client. The installed default reads <stateDir>/connection.json, then GETs
// /api/v1/snapshot and GETs /api/v1/events. No service.json and no
// `opencode api session.list` are involved in the live branch (ADR 0191).
// ---------------------------------------------------------------------------

// watchLiveRunner is the --live entry point. It is a seam so the legacy loop
// remains testable; production uses the HTTP projection.
var watchLiveRunner = func(c *CLI, interval time.Duration, format string, wake bool) int {
	return runWatchLiveHTTPWithDeps(c, interval, format, wake, defaultWatchLiveDeps())
}

// watchHTTPPort is the whole live dependency: one snapshot and one SSE stream.
// httpapi.ProjectionClient implements it; tests substitute a stand-in.
type watchHTTPPort interface {
	Snapshot(context.Context) (httpapi.ClientSnapshot, error)
	Events(context.Context) <-chan httpapi.ClientEventResult
}

var (
	watchHTTPStateDir = func() (string, error) { return stateDir() }
	watchHTTPClient   = func(conn httpapi.ClientConn) (watchHTTPPort, error) {
		return httpapi.NewProjectionClient(conn, nil)
	}
)

// joinDash separates non-empty items and prints a dash for an empty list.
func joinDash(values []string) string {
	kept := make([]string, 0, len(values))
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	if len(kept) == 0 {
		return "-"
	}
	return strings.Join(kept, "; ")
}

// statusWithBlocker folds the optional blocker into STATUS(BLOCKER).
func statusWithBlocker(status, blocker string) string {
	if blocker == "" {
		return status
	}
	if status == "" {
		return "blocked(" + blocker + ")"
	}
	return status + "(" + blocker + ")"
}

// subscriberWithPhase folds the optional phase into SUBSCRIBER(PHASE).
func subscriberWithPhase(subscriber, phase string) string {
	if phase == "" {
		return subscriber
	}
	if subscriber == "" {
		return "(" + phase + ")"
	}
	return subscriber + "(" + phase + ")"
}

// renderHTTPSnapshot prints one snapshot: a header, the joined watch table and
// the attention/escalation lines. json stays machine readable and bypasses the
// human layout.
func renderHTTPSnapshot(w io.Writer, snap httpapi.ClientSnapshot, format string) error {
	if format == "json" {
		return json.NewEncoder(w).Encode(snap)
	}

	project := dash(strings.TrimSpace(snap.Project))
	points := snap.Points
	if points.Limit <= 0 {
		points.Limit = 500
	}
	cycle := dash(strings.TrimSpace(snap.Cycle.ID))
	if strings.TrimSpace(snap.Cycle.Duration) != "" {
		cycle += " (" + snap.Cycle.Duration + ")"
	}
	orchestrator := "offline"
	if snap.Orchestrator.Live {
		orchestrator = "live"
		if snap.Orchestrator.Session != "" {
			orchestrator += " " + snap.Orchestrator.Session
		}
	}
	fmt.Fprintf(w, "PROJECT: %s\tPOINTS: %d/%d\tCYCLE: %s\tORCHESTRATOR: %s\n",
		project, points.Used, points.Limit, cycle, orchestrator)

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PR\tWATCH\tVERDICT\tSTATUS(BLOCKER)\tSUBSCRIBER(PHASE)\tFIX+WAKE ATTEMPTS\tUPDATED")
	for _, row := range snap.Watches {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d+%d\t%s\n",
			dash(row.PR), dash(row.Watch), dash(row.Verdict),
			dash(statusWithBlocker(row.Status, row.Blocker)),
			dash(subscriberWithPhase(row.Subscriber, row.Phase)),
			row.FixAttempts, row.WakeAttempts, dash(row.Updated))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "ATTENTION: %s\n", joinDash(snap.Attention))
	fmt.Fprintf(w, "ESCALATIONS: %s\n", joinDash(snap.Escalations))
	fmt.Fprintf(w, "EVENTS: %s\n", joinDash(snap.EventTail))
	return nil
}

// watchHTTPRender is one refresh from the HTTP projection. tail holds the
// newest replayed event lines and is attached to the snapshot so the human
// layout and the JSON form carry the same cold-start history.
func watchHTTPRender(ctx context.Context, c *CLI, port watchHTTPPort, format string, tail []string) int {
	snap, err := port.Snapshot(ctx)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon watch: snapshot: %v\n", err)
		return 2
	}
	snap.EventTail = tail
	if err := renderHTTPSnapshot(c.out, snap, format); err != nil {
		fmt.Fprintf(c.errOut, "beacon watch: %v\n", err)
		return 2
	}
	return 0
}

// appendEventTail formats one SSE frame and keeps at most limit lines, oldest
// first. It mirrors the board's event line so both live views read the same way.
func appendEventTail(tail []string, ev httpapi.ClientEvent, limit int) []string {
	typ := strings.TrimSpace(ev.Type)
	if typ == "" {
		typ = "message"
	}
	data := strings.Join(strings.Fields(strings.TrimSpace(string(ev.Data))), " ")
	if data == "" {
		data = "(пусто)"
	}
	line := typ + " " + data
	if id := strings.TrimSpace(ev.ID); id != "" {
		line = "#" + id + " " + line
	}
	tail = append(tail, line)
	if len(tail) > limit {
		tail = tail[len(tail)-limit:]
	}
	return tail
}

// runWatchLiveHTTPWithDeps is the event-driven loop over /api/v1/events. Each
// subscription starts from a fresh snapshot; every frame, reconnect or
// interval tick prints a new one. The SSE stream carries no history, so missed
// events are never replayed (ADR 0025). Its seams arrive in deps so callers do
// not have to replace package globals.
func runWatchLiveHTTPWithDeps(c *CLI, interval time.Duration, format string, wake bool, deps watchLiveDeps) int {
	dir, err := deps.stateDir()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon watch: --live: state dir: %v\n", err)
		return 2
	}
	conn, err := httpapi.LoadClientConn(dir)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon watch: --live: connection.json: %v\n", err)
		return 2
	}
	port, err := deps.client(conn)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon watch: --live: http client: %v\n", err)
		return 2
	}

	ctx, cancel := deps.lifetime()
	defer cancel()

	var ticks <-chan time.Time
	if interval > 0 {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		ticks = ticker.C
	}

	_ = wake // batching belongs to the legacy wake path, not this projection.
	var tail []string
	for {
		if ctx.Err() != nil {
			return 0
		}
		events := port.Events(ctx)
		if code := watchHTTPRender(ctx, c, port, format, tail); code != 0 {
			return code
		}
		disconnected := false
		for !disconnected {
			select {
			case <-ctx.Done():
				return 0
			case result, ok := <-events:
				if !ok {
					disconnected = true
					continue
				}
				if result.Err == nil {
					tail = appendEventTail(tail, result.Event, eventTailMax)
				}
				_ = watchHTTPRender(ctx, c, port, format, tail)
			case <-ticks:
				_ = watchHTTPRender(ctx, c, port, format, tail)
			}
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(deps.reconnectDelay):
		}
	}
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
	if !parseFormatFlag(*format, "beacon gc: неизвестный формат %q, ожидается table или json\n", c.errOut, true) {
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

// ---------------------------------------------------------------------------
// T3.125 / ADR 0056 p4: батчинг событий в одно пробуждение сессии.
//
// События, пришедшие в одном окне 200 мс, сливаются в один wake сессии. Это
// ограничивает кратность пробуждения, когда лог сыплет строками. Механизм не
// добавляет ступени в лестницу ADR 0017 и не разносит пробуждения во времени,
// как wake_between из T3.80: все события окна дают ровно один wake.
// ---------------------------------------------------------------------------

// wakeBatchWindow is the burst window of ADR 0056 p4. Events of one session
// inside this window produce a single wake.
const wakeBatchWindow = 200 * time.Millisecond

// wakeEvent is one notification that would otherwise wake a session by itself.
// At is the arrival time used to decide which 200ms window the event belongs
// to, Target is the session and Text is the detail carried into the wake.
type wakeEvent struct {
	At     time.Time
	Target string
	Text   string
}

// wakeBatch is the collapsed result: one wake for Target carrying every event
// text of the window in arrival order.
type wakeBatch struct {
	Target string
	Texts  []string
}

type openWakeBatch struct {
	at    time.Time
	texts []string
}

// batchWakeEvents groups events by target session into windows of `window`
// measured from that session's first event. An event joins the open window
// while its timestamp is before open+window; at or after that point it closes
// the current batch and opens the next one. Output is ordered by window open
// time then target, so the function is deterministic and never staggers wakes
// in time the way T3.80 does.
func batchWakeEvents(events []wakeEvent, window time.Duration) []wakeBatch {
	open := make(map[string]*openWakeBatch)
	type closed struct {
		at     time.Time
		target string
		texts  []string
	}
	var batches []closed
	closeBatch := func(target string) {
		p := open[target]
		batches = append(batches, closed{at: p.at, target: target, texts: p.texts})
		delete(open, target)
	}
	for _, ev := range events {
		if p, ok := open[ev.Target]; ok && !ev.At.Before(p.at.Add(window)) {
			closeBatch(ev.Target)
		}
		p, ok := open[ev.Target]
		if !ok {
			open[ev.Target] = &openWakeBatch{at: ev.At}
			p = open[ev.Target]
		}
		p.texts = append(p.texts, ev.Text)
	}
	for target := range open {
		closeBatch(target)
	}
	sort.Slice(batches, func(i, j int) bool {
		if batches[i].at.Equal(batches[j].at) {
			return batches[i].target < batches[j].target
		}
		return batches[i].at.Before(batches[j].at)
	})
	out := make([]wakeBatch, 0, len(batches))
	for _, b := range batches {
		out = append(out, wakeBatch{Target: b.target, Texts: b.texts})
	}
	return out
}

// wakeSend delivers one collapsed wake. watch.go does not own a wake tool;
// delivery of the follow-up stays with ADR 0026. The sender is injected so
// tests can count wakes without a live session.
type wakeSend func(target, text string) error

// emitWakeBatches sends one wake per batch. The texts of a burst are joined
// into a single follow-up so the session wakes once and reads the whole burst.
func emitWakeBatches(batches []wakeBatch, send wakeSend) error {
	for _, b := range batches {
		if err := send(b.Target, strings.Join(b.Texts, "; ")); err != nil {
			return err
		}
	}
	return nil
}

// runWatchBatchedWake turns the joined watch rows into wake events and emits
// one wake per session per 200ms window. Only rows with a subscriber can wake
// a session. now is injected so tests do not depend on wall time.
func runWatchBatchedWake(rows []watchRow, now time.Time, send wakeSend) error {
	events := make([]wakeEvent, 0, len(rows))
	for _, r := range rows {
		if r.Subscriber == "" {
			continue
		}
		events = append(events, wakeEvent{
			At:     now,
			Target: r.Subscriber,
			Text:   fmt.Sprintf("%s@%d", r.Verdict, r.Revision),
		})
	}
	return emitWakeBatches(batchWakeEvents(events, wakeBatchWindow), send)
}

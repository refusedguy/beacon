// Package mcp: beacon_list_running as a pure read of Beacon state.
package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// StateSnapshot is the read-only view of Beacon state shared by the two
// observability tools. A SnapshotReader produces it. No field is sourced from
// a write path, so the tools only project what was already persisted.
type StateSnapshot struct {
	ActiveWatches []domain.Watch
	// Sessions carries the disk signals used to decide session liveness
	// without probing processes (ADR 0036 §4). The wiring layer fills it when
	// session artifacts and recorded PIDs are available; a snapshot without
	// sessions keeps the previous behavior.
	Sessions        []SessionLiveness
	PointsThisHour  int
	Budget          int
	PersistSeq      uint64
	LastPersistErr  string
	LoadErrors      []string
	LadderPending   int
	LadderEscalated int
	Version         string
}

// StatusIdle is the status beacon_list_running reports for a watch whose agent
// session is gone. It is a projection only: the store keeps the original
// lifecycle status and never receives idle as a domain.Status (ADR 0036 §4).
const StatusIdle = "idle"

// SessionLiveness holds the disk signals used to decide whether an agent
// session is still live (ADR 0036 §4) and whether its task has died (ADR 0042
// §2). SessionID matches a subscriber's Target. Artifacts are paths Beacon
// expects on disk while the session is active. PID is the process recorded
// when the session started. LastActivity and LastHeartbeat are the two signals
// added by ADR 0042 §2; the wiring layer fills them from persisted state, and
// a zero value means the signal is unknown.
type SessionLiveness struct {
	SessionID     string    `json:"sessionID"`
	Artifacts     []string  `json:"artifacts"`
	PID           int       `json:"pid"`
	LastActivity  time.Time `json:"lastActivity"`
	LastHeartbeat time.Time `json:"lastHeartbeat"`
}

// SnapshotReader is the single read-only state accessor used by
// beacon_list_running and beacon_stats. The interface deliberately has no
// write method: a tool holding only this dependency cannot mutate state, which
// is non-variant 9 of WORKER-PROMPT §4.2.
type SnapshotReader interface {
	Snapshot() (StateSnapshot, error)
}

// SnapshotFunc adapts a read function to SnapshotReader. The wiring layer can
// pass a closure over the store, the point counter and the ladder reader
// without adding another type to this package.
type SnapshotFunc func() (StateSnapshot, error)

func (f SnapshotFunc) Snapshot() (StateSnapshot, error) { return f() }

// ListRunningFilters narrows the watches returned by beacon_list_running.
// Empty fields are ignored, so an empty filter selects every running watch.
type ListRunningFilters struct {
	Verdict string `json:"verdict,omitempty" jsonschema:"only watches with this verdict"`
	Status  string `json:"status,omitempty" jsonschema:"only watches with this status"`
}

func (f *ListRunningFilters) matches(w domain.Watch) bool {
	if f.Verdict != "" && string(w.Verdict) != f.Verdict {
		return false
	}
	if f.Status != "" && string(w.Status) != f.Status {
		return false
	}
	return true
}

// ListRunningInput is the input of beacon_list_running. Filters and Cursor are
// optional: an empty Cursor selects every current event (ADR 0025).
type ListRunningInput struct {
	Filters *ListRunningFilters `json:"filters,omitempty" jsonschema:"optional filter on running watches"`
	Cursor  string              `json:"cursor,omitempty" jsonschema:"opaque cursor from a previous response"`
}

// ListRunningHandler implements beacon_list_running over the read-only
// snapshot port. It holds no writer, so the handler cannot mutate Beacon state.
// Alongside the reader it keeps only an event cursor and the terminal verdict
// (ADR 0025): no stream history is retained.
type ListRunningHandler struct {
	reader SnapshotReader

	mu              sync.Mutex
	cursor          uint64
	terminalStatus  string
	terminalVerdict string
}

// NewListRunningHandler builds the handler. Pass handler.Handle to
// mcpsdk.AddTool when wiring the tool into a server.
func NewListRunningHandler(reader SnapshotReader) *ListRunningHandler {
	return &ListRunningHandler{reader: reader}
}

// encodeEventCursor turns a monotonic event position into an opaque string
// suitable for Last-Event-ID. Clients never have to interpret it.
func encodeEventCursor(seq uint64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatUint(seq, 10)))
}

// decodeEventCursor reverses encodeEventCursor. An empty cursor means "from
// the beginning" and decodes to zero; a malformed value is an input error.
func decodeEventCursor(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, fmt.Errorf("invalid cursor: %w", err)
	}
	seq, err := strconv.ParseUint(string(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid cursor: %w", err)
	}
	return seq, nil
}

const (
	// activityStaleAfter is how old the last recorded activity may be before
	// the activity signal indicates a stalled task (ADR 0042 §2).
	activityStaleAfter = 5 * time.Minute
	// heartbeatStaleAfter is how old the last heartbeat may be before the
	// heartbeat signal indicates a stalled task (ADR 0042 §2). A heartbeat is
	// expected more often than real activity, hence the shorter bound.
	heartbeatStaleAfter = 2 * time.Minute
)

// currentTime is the clock used to age the activity and heartbeat signals.
// It is a variable so tests can freeze time; production uses time.Now.
var currentTime = time.Now

// processAlive checks one recorded PID with signal 0. The kernel reports
// whether the process exists without delivering a signal, so this neither
// enumerates processes nor starts ps (ADR 0036 §4). EPERM still means the
// process is alive but owned by another user. The variable is replaceable in
// tests; production uses the same syscall the taskstore lock uses.
var processAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// artifactsPresent reports whether every expected artifact path is a regular
// file on disk. An empty list adds no requirement.
func artifactsPresent(paths []string) bool {
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

// sessionDead is the compound death detector from ADR 0042 §2. It reads all
// three signals together and needs a recorded PID to run. A dead PID is the
// quick sign inherited from T3.44. When the PID is live, a task that has both
// stalled activity and a stale heartbeat is dead too: a live PID alone does
// not prove the task is working (ADR 0042 §2). Unknown time signals never
// contribute a death vote, so a snapshot without them keeps the T3.44
// behavior.
func sessionDead(s SessionLiveness) bool {
	// Without a recorded PID the compound detector has only two of its three
	// signals, which is not enough to declare the task dead.
	if s.PID == 0 {
		return false
	}
	if !processAlive(s.PID) {
		return true
	}
	if s.LastActivity.IsZero() || s.LastHeartbeat.IsZero() {
		return false
	}
	now := currentTime()
	return now.Sub(s.LastActivity) > activityStaleAfter &&
		now.Sub(s.LastHeartbeat) > heartbeatStaleAfter
}

// sessionLive combines the liveness signals from ADR 0036 §4 and the compound
// death detector from ADR 0042 §2. A session the detector calls dead is idle at
// once. Otherwise the artifact files decide. A session with no signals at all
// is unknown and is left alone, so callers that supply no liveness data keep
// the previous behavior.
func sessionLive(s SessionLiveness) bool {
	if s.PID == 0 && len(s.Artifacts) == 0 && s.LastActivity.IsZero() && s.LastHeartbeat.IsZero() {
		return true
	}
	if sessionDead(s) {
		return false
	}
	return artifactsPresent(s.Artifacts)
}

// effectiveStatus projects a stored watch status into the status Beacon
// reports. A closed watch stays closed. Otherwise, if any subscriber belongs
// to a session that is no longer live, the watch is idle (ADR 0036 §4, ADR
// 0042 §2).
func effectiveStatus(w domain.Watch, sessions map[string]SessionLiveness) domain.Status {
	if w.Status == domain.StatusClosed {
		return w.Status
	}
	for _, sub := range w.Subscribers {
		s, ok := sessions[sub.Target]
		if ok && !sessionLive(s) {
			return domain.Status(StatusIdle)
		}
	}
	return w.Status
}

// statusRank orders the projected statuses: closed outranks idle, idle
// outranks notifying and notifying outranks watching.
func statusRank(s string) int {
	switch domain.Status(s) {
	case domain.StatusClosed:
		return 3
	case StatusIdle:
		return 2
	case domain.StatusNotifying:
		return 1
	default:
		return 0
	}
}

// aggregateStatus returns the lifecycle status implied by the observed events.
// A closed watch wins over idle, which wins over notifying, which wins over
// watching.
func aggregateStatus(events []WatchEvent) string {
	status := string(domain.StatusWatching)
	for _, ev := range events {
		if statusRank(ev.Status) > statusRank(status) {
			status = ev.Status
		}
	}
	return status
}

// Handle reads a snapshot and projects it onto counts, a watch array and a
// cursor-delimited event stream. Every counter is derived from the returned
// array, so counts stay consistent with watches for any filter. With an empty
// input cursor the read is stateless and returns every current event; with a
// cursor it returns only events newer than that position (ADR 0025).
func (h *ListRunningHandler) Handle(_ context.Context, _ *mcpsdk.CallToolRequest, in ListRunningInput) (*mcpsdk.CallToolResult, ListRunningOutput, error) {
	from, err := decodeEventCursor(in.Cursor)
	if err != nil {
		return nil, ListRunningOutput{}, domain.NewAppError(domain.CodeInvalidInput, err.Error())
	}

	snap, err := h.reader.Snapshot()
	if err != nil {
		return nil, ListRunningOutput{}, fmt.Errorf("beacon_list_running: %w", err)
	}

	sessions := make(map[string]SessionLiveness, len(snap.Sessions))
	for _, s := range snap.Sessions {
		sessions[s.SessionID] = s
	}

	watches := make([]domain.Watch, 0, len(snap.ActiveWatches))
	for _, w := range snap.ActiveWatches {
		if in.Filters != nil && !in.Filters.matches(w) {
			continue
		}
		w.Status = effectiveStatus(w, sessions)
		watches = append(watches, w)
	}

	byVerdict := make(map[string]int)
	subscribers := 0
	for _, w := range watches {
		byVerdict[string(w.Verdict)]++
		subscribers += len(w.Subscribers)
	}

	// Events are a position-indexed view over the snapshot. Seq is the 1-based
	// position, so a cursor stays opaque to clients yet stable within one
	// stream. No event history is stored between calls.
	events := make([]WatchEvent, 0, len(snap.ActiveWatches))
	terminalStatus, terminalVerdict := "", ""
	for i, w := range snap.ActiveWatches {
		ev := WatchEvent{
			Seq:     uint64(i + 1),
			WatchID: w.ID,
			Status:  string(effectiveStatus(w, sessions)),
			Verdict: string(w.Verdict),
		}
		events = append(events, ev)
		if terminalVerdict == "" && w.Status == domain.StatusClosed {
			terminalStatus = string(w.Status)
			terminalVerdict = string(w.Verdict)
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Remember only the cursor and the terminal verdict, never the history.
	if terminalVerdict != "" {
		h.terminalStatus = terminalStatus
		h.terminalVerdict = terminalVerdict
	}

	newEvents := make([]WatchEvent, 0, len(events))
	if in.Cursor == "" {
		// Stateless full read: every current event, no cursor state consumed.
		newEvents = append(newEvents, events...)
	} else {
		// A cursor call returns only events past the client cursor and past the
		// last delivered position, so a reconnect never replays old events.
		floor := from
		if h.cursor > floor {
			floor = h.cursor
		}
		for _, ev := range events {
			if ev.Seq > floor {
				newEvents = append(newEvents, ev)
			}
		}
		if uint64(len(events)) > h.cursor {
			h.cursor = uint64(len(events))
		}
	}

	out := ListRunningOutput{
		Counts: WatchCounts{
			Watches:     len(watches),
			ByVerdict:   byVerdict,
			Subscribers: subscribers,
		},
		Watches: watches,
		Status:  aggregateStatus(events),
		Cursor:  encodeEventCursor(uint64(len(events))),
		Events:  newEvents,
	}

	if h.terminalVerdict != "" {
		out.Status = h.terminalStatus
		out.Verdict = h.terminalVerdict
		out.Events = nil
	}

	return &mcpsdk.CallToolResult{}, out, nil
}

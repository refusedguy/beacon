// Package observe: agent turn end events.
//
// A coding agent hook writes one JSON object when its turn ends. The reader of
// that file and the hook installation stay at the caller boundary. This file
// owns only the wire type, its validation, and the conversion into a session
// status transition. It reads no process state and opens no file, so a caller
// never needs to poll a process to learn that a turn ended.
package observe

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// AgentTurnStop is the hook event name that marks the end of a turn. Other
// hook events are ignored by the tracker.
const AgentTurnStop = "Stop"

// AgentSessionIdle is the status a Stop transition asks the registry to write.
// The literal matches the idle status already used by the session registry;
// this package keeps its own copy so it needs no import from the store.
const AgentSessionIdle = "idle"

// Reason values returned by AgentTurnTracker.Apply. A caller that records the
// reason can tell a rejected event from one it already applied.
const (
	AgentTurnApplied     = "applied"
	AgentTurnNotStop     = "not_stop"
	AgentTurnForeignPane = "foreign_pane"
	AgentTurnDuplicate   = "duplicate"
)

// Validation errors of an incoming hook payload. Each one is a sentinel so a
// boundary caller can classify a rejection without parsing the message.
var (
	ErrAgentTurnJSON  = errors.New("observe: agent turn: invalid JSON")
	ErrAgentTurnEvent = errors.New("observe: agent turn: missing event")
	ErrAgentTurnPane  = errors.New("observe: agent turn: missing pane id")
	ErrAgentTurnTurn  = errors.New("observe: agent turn: missing turn id")
)

// AgentTurnEvent is one validated hook payload. PaneID and TurnID identify the
// delivery. SessionID and TranscriptPath are carried through to the transition
// so the boundary can attach them to the session record. Timestamp is the time
// written by the hook; a zero value means the hook omitted it.
type AgentTurnEvent struct {
	Event          string
	PaneID         string
	TurnID         string
	SessionID      string
	TranscriptPath string
	Timestamp      time.Time
}

// ParseAgentTurnEvent validates one JSON payload from an agent hook. It accepts
// the event name under "event" or "hook_event_name", because coding agents use
// both spellings. The pane id and turn id are required: without them a
// delivery cannot be matched to a pane or deduplicated. An unknown timestamp
// format is rejected instead of being replaced with the local clock, so the
// record keeps the time the hook wrote.
func ParseAgentTurnEvent(data []byte) (AgentTurnEvent, error) {
	var raw struct {
		Event          string `json:"event"`
		HookEventName  string `json:"hook_event_name"`
		PaneID         string `json:"pane_id"`
		TurnID         string `json:"turn_id"`
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		Timestamp      string `json:"timestamp"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return AgentTurnEvent{}, fmt.Errorf("%w: %v", ErrAgentTurnJSON, err)
	}

	name := strings.TrimSpace(raw.Event)
	if name == "" {
		name = strings.TrimSpace(raw.HookEventName)
	}
	if name == "" {
		return AgentTurnEvent{}, ErrAgentTurnEvent
	}
	pane := strings.TrimSpace(raw.PaneID)
	if pane == "" {
		return AgentTurnEvent{}, ErrAgentTurnPane
	}
	turn := strings.TrimSpace(raw.TurnID)
	if turn == "" {
		return AgentTurnEvent{}, ErrAgentTurnTurn
	}

	e := AgentTurnEvent{
		Event:          name,
		PaneID:         pane,
		TurnID:         turn,
		SessionID:      strings.TrimSpace(raw.SessionID),
		TranscriptPath: strings.TrimSpace(raw.TranscriptPath),
	}
	if stamp := strings.TrimSpace(raw.Timestamp); stamp != "" {
		parsed, err := time.Parse(time.RFC3339, stamp)
		if err != nil {
			return AgentTurnEvent{}, fmt.Errorf("%w: timestamp %q: %v", ErrAgentTurnJSON, stamp, err)
		}
		e.Timestamp = parsed
	}
	return e, nil
}

// SessionStatusTransition is the status change an event asks for. The boundary
// caller applies it to the session registry. This package never writes to that
// registry, so an agent turn end cannot change a status without a caller.
type SessionStatusTransition struct {
	SessionID      string
	Status         string
	TurnID         string
	TranscriptPath string
	At             time.Time
}

// AgentTurnResult is the outcome of one delivery. Transition is meaningful
// only when Applied is true. Reason is always one of the AgentTurn* reason
// values above.
type AgentTurnResult struct {
	Transition SessionStatusTransition
	Applied    bool
	Reason     string
}

// maxAgentTurnSeen bounds the dedup window. A tracker that sees a fresh turn on
// every Stop would otherwise grow for the lifetime of the process. The value
// matches maxWaitStarts in the MCP wait registry and covers many panes over
// many turns. A replay of a key that has been evicted is treated as a new
// delivery.
const maxAgentTurnSeen = 4096

// AgentTurnTracker converts validated events into session status transitions.
// It remembers delivered turn ids so a hook that replays an event cannot make
// the caller write a second transition. One tracker can serve several panes:
// the dedup key includes the pane id. The window holds at most maxAgentTurnSeen
// keys, with the oldest key removed first.
type AgentTurnTracker struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	order []string // dedup keys in insertion order, for oldest-first eviction
}

// NewAgentTurnTracker returns an empty tracker.
func NewAgentTurnTracker() *AgentTurnTracker {
	return &AgentTurnTracker{seen: make(map[string]struct{})}
}

// Apply converts e into a transition for paneID. A delivery is applied only
// when it is a Stop event and its pane id matches the pane the caller owns.
// The first delivery of a turn is applied; a replay of the same pane id and
// turn id returns AgentTurnDuplicate with no transition. A rejected delivery
// is not remembered, so a later valid delivery with the same ids is still
// applied.
func (t *AgentTurnTracker) Apply(e AgentTurnEvent, paneID string) AgentTurnResult {
	if e.Event != AgentTurnStop {
		return AgentTurnResult{Reason: AgentTurnNotStop}
	}
	if e.PaneID == "" || e.PaneID != paneID {
		return AgentTurnResult{Reason: AgentTurnForeignPane}
	}

	key := e.PaneID + "\x00" + e.TurnID
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = make(map[string]struct{})
	}
	if _, ok := t.seen[key]; ok {
		return AgentTurnResult{Reason: AgentTurnDuplicate}
	}
	t.seen[key] = struct{}{}
	t.order = append(t.order, key)
	if len(t.order) > maxAgentTurnSeen {
		oldest := t.order[0]
		t.order = t.order[1:]
		delete(t.seen, oldest)
	}

	at := e.Timestamp
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return AgentTurnResult{
		Applied: true,
		Reason:  AgentTurnApplied,
		Transition: SessionStatusTransition{
			SessionID:      e.SessionID,
			Status:         AgentSessionIdle,
			TurnID:         e.TurnID,
			TranscriptPath: e.TranscriptPath,
			At:             at,
		},
	}
}

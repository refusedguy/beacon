// Package projection holds the read and write ports shared by the HTTP
// transport and a future UI. The package imports only the standard library.
// Domain values stay in the adapter and arrive as type arguments, so no store
// or transport type leaks into this layer.
package projection

import (
	"context"
	"encoding/json"
	"time"
)

// PointStats is the hourly point budget already computed by the CI provider.
type PointStats struct {
	Used   int `json:"used"`
	Budget int `json:"budget"`
}

// LastCycle is the most recent completed reconciler pass.
type LastCycle struct {
	At             time.Time `json:"at,omitempty"`
	WatchesUpdated int       `json:"watchesUpdated"`
	DurationMs     int64     `json:"durationMs"`
}

// WatchOutcome stays a string so the OpenAPI enum does not change when the
// domain gains new verdicts.
type WatchOutcome string

const (
	OutcomeMerged    WatchOutcome = "merged"
	OutcomeClosed    WatchOutcome = "closed"
	OutcomeEscalated WatchOutcome = "escalated"
)

// WatchHistory is one completed Watch projected for a UI. It is a copy of the
// wire shape, never a live object, so a domain change cannot change the JSON.
type WatchHistory struct {
	ID          string       `json:"id"`
	Repo        string       `json:"repo"`
	PR          int          `json:"pr"`
	HeadSHA     string       `json:"headSHA"`
	Outcome     WatchOutcome `json:"outcome"`
	ClosedAt    time.Time    `json:"closedAt"`
	CloseReason string       `json:"closeReason,omitempty"`
}

// HourlyStats is the last hour's activity. Points come from the CI provider,
// merges from completed Watches and blockers from the active incidents.
type HourlyStats struct {
	Points   int `json:"points"`
	Merges   int `json:"merges"`
	Blockers int `json:"blockers"`
}

// QueueBlock is the queue panel of the live screen. Todo holds ready cards and
// Deferred holds cards parked by an earlier cycle.
type QueueBlock struct {
	Todo     []QueueCard `json:"todo,omitempty"`
	Deferred []QueueCard `json:"deferred,omitempty"`
}

// QueueCard is one card in the queue panel. Owner and Phase stay optional for
// older cards.
type QueueCard struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Owner  string `json:"owner,omitempty"`
	Title  string `json:"title"`
	Phase  string `json:"phase,omitempty"`
}

// SlotBlock reports the orchestrator slot capacity shown by the live screen.
type SlotBlock struct {
	Total int `json:"total"`
	Used  int `json:"used"`
}

// WorkerBlock is one executor listed with its role and wake target.
type WorkerBlock struct {
	ID     string `json:"id"`
	Role   string `json:"role,omitempty"`
	Target string `json:"target,omitempty"`
}

// SnapshotHistory is the optional history block added to a Snapshot.
type SnapshotHistory struct {
	RecentWatches []WatchHistory `json:"recentWatches"`
	HourlyStats   HourlyStats    `json:"hourlyStats"`
}

// Snapshot is the read model served by /api/v1/snapshot. W, B and O are the
// adapter's own watch, blocker and binding types. Passing them as type
// arguments keeps this package stdlib-only while the adapter keeps its exact
// wire shape. Optional fields stay absent from the JSON when unset.
type Snapshot[W, B, O any] struct {
	LiveWatches            []W             `json:"liveWatches"`
	BlockerIncidents       []B             `json:"blockerIncidents"`
	BoundOrchestrator      *O              `json:"boundOrchestrator,omitempty"`
	Escalations            []string        `json:"escalations"`
	PointStats             PointStats      `json:"pointStats"`
	Metrics                json.RawMessage `json:"metrics"`
	LastCycle              LastCycle       `json:"lastCycle"`
	LoadErrors             []string        `json:"loadErrors"`
	RecentWatches          []WatchHistory  `json:"recentWatches,omitempty"`
	HourlyStats            *HourlyStats    `json:"hourlyStats,omitempty"`
	RecentWatchesTruncated bool            `json:"recentWatchesTruncated,omitempty"`
	Queue                  *QueueBlock     `json:"queue,omitempty"`
	Slots                  *SlotBlock      `json:"slots,omitempty"`
	Workers                []WorkerBlock   `json:"workers,omitempty"`
}

// Decision is one human command. ObligationID and Note are optional. Actor is
// recorded by the sink, not interpreted by the transport. Contract names the
// wire contract version; an empty value keeps the pre-versioning shape valid.
// AppKey identifies the application that sent the command. An empty value
// keeps the local TUI path unchanged.
type Decision struct {
	Command      string `json:"command"`
	WatchID      string `json:"watchID"`
	ObligationID string `json:"obligationID,omitempty"`
	Note         string `json:"note,omitempty"`
	Actor        string `json:"actor,omitempty"`
	Contract     string `json:"contract,omitempty"`
	AppKey       string `json:"appKey,omitempty"`
}

// DecisionResult is returned for every accepted command. Applied is false on
// an idempotent repeat so a client can tell a fresh effect from a cached one.
type DecisionResult struct {
	OK         bool   `json:"ok"`
	Command    string `json:"command"`
	WatchID    string `json:"watchID"`
	Applied    bool   `json:"applied"`
	DecisionID string `json:"decisionID"`
	Status     string `json:"status,omitempty"`
	Contract   string `json:"contract,omitempty"`
}

// StateProvider is the read port. S is the adapter's Snapshot type.
type StateProvider[S any] interface {
	Snapshot(ctx context.Context) (S, error)
}

// DecisionSink is the write port. D and R are the adapter's Decision and
// DecisionResult types. Idempotency stays on the server side.
type DecisionSink[D, R any] interface {
	Apply(ctx context.Context, d D) (R, error)
}

// Event is one frame on the event stream. ID doubles as Last-Event-ID on
// reconnect.
type Event struct {
	ID      uint64          `json:"id"`
	Type    string          `json:"type"`
	At      time.Time       `json:"at"`
	WatchID string          `json:"watchID,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// EventSource is the read port for live and replayed frames. A subscriber owns
// the returned stop function. Since returns buffered frames with an id greater
// than the one the client already saw.
type EventSource[E any] interface {
	Subscribe() (<-chan E, func())
	Since(lastID uint64) []E
}

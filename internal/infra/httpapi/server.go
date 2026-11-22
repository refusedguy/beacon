// Package httpapi serves a loopback-only HTTP and SSE view of Beacon state.
// It owns no store handle: the composition root injects a read port and a
// command sink, so the transport stays independent of the persistence layer.
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

const (
	// loopbackAddr is used when the caller does not set BEACON_HTTP_ADDR. Port
	// zero lets the OS pick a free port, which the connection file reports.
	loopbackAddr = "127.0.0.1:0"
	// AddrEnv overrides the listen address. The host part is forced to
	// loopback, so a bad setting cannot expose the API on a public interface.
	AddrEnv = "BEACON_HTTP_ADDR"

	connectionFileName = "connection.json"
	connectionPerm     = 0o600

	defaultEventCapacity = 256
	subscriberBuffer     = 64
	maxReplay            = 1000
	maxRequestBody       = 1 << 20
	shutdownTimeout      = 3 * time.Second

	// maxRecentWatches bounds the history block on the snapshot. A provider may
	// keep more completed Watches in SQLite; the transport publishes the newest
	// maxRecentWatches of them so a busy project cannot grow the response without
	// limit.
	maxRecentWatches = 30

	// maxAuditRecords bounds the in-process audit list exposed by /api/v1/audit.
	// ATX-41 raises the old 200-entry cap so a normal command history stays
	// reachable in full. Older entries are returned through a cursor instead of
	// being trimmed at 200. The cap still bounds memory in a long-lived process.
	maxAuditRecords = 100000
	// defaultAuditPageSize is the page size when a request omits limit. It keeps
	// the old behaviour of returning up to 200 entries in one response.
	defaultAuditPageSize = 200
	// maxAuditPageSize bounds one page so one call cannot grow without limit.
	maxAuditPageSize = 500

	// ingressHTTPAPI marks commands recorded by this transport in the audit
	// projection. The other ingresses (mcp, tui, webhook) belong to other
	// transports and are not visible here.
	ingressHTTPAPI = "httpapi"

	// HumanDecideContract is the versioned wire contract of the human-answer
	// projection. A client may omit the contract field to keep the original
	// shape working; when present it must equal this value.
	HumanDecideContract = "beacon.human-decide.v1"

	// EventContract is the versioned wire contract of the typed SSE envelope.
	// Every typed payload carries a contract field, and a value other than this
	// one is rejected before it reaches the bus. The legacy frames without a
	// contract keep working, so an older subscriber sees the same stream.
	EventContract = "beacon.events.v1"
)

// heartbeatInterval is a variable so a test can shorten it without waiting a
// real quarter minute for the keepalive comment. The production value is 15s.
var heartbeatInterval = 15 * time.Second

// maxPatchesPerSnapshot bounds a patch chain. A producer must publish a full
// snapshot after this many patches so a newcomer never has to replay an
// unbounded sequence of deltas. It is a variable so a test can shorten the
// chain without emitting hundreds of frames.
var maxPatchesPerSnapshot = 32

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

// VersionInfo is the response of GET /api/v1/version. It reports the same
// build facts as /api/v1/health without requiring a provider read.
type VersionInfo struct {
	OK         bool      `json:"ok"`
	Version    string    `json:"version"`
	PID        int       `json:"pid"`
	ProjectKey string    `json:"projectKey"`
	StartedAt  time.Time `json:"startedAt"`
	UptimeSec  int64     `json:"uptimeSec"`
}

// OpsMetrics is the response of GET /api/v1/metrics. ActiveTasks is the number
// of unfinished runs (live Watches) read from the state provider; the wake
// fields summarise wakes observed through ObserveWake since process start.
type OpsMetrics struct {
	OK                bool    `json:"ok"`
	ActiveTasks       int     `json:"active_tasks"`
	WakesTotal        uint64  `json:"wakes_total"`
	WakeLatencyMs     float64 `json:"wake_latency_ms"`
	WakeLatencyMaxMs  float64 `json:"wake_latency_max_ms"`
	WakeLatencyLastMs float64 `json:"wake_latency_last_ms"`
	UptimeSec         int64   `json:"uptime_sec"`
}

// AuditRecord is one privileged command recorded by this transport and
// exposed by GET /api/v1/audit. Actor and Ingress are always present so an
// operator can tell who acted and where the command arrived.
type AuditRecord struct {
	Actor      string    `json:"actor"`
	Ingress    string    `json:"ingress"`
	Command    string    `json:"command"`
	WatchID    string    `json:"watchID"`
	DecisionID string    `json:"decisionID"`
	At         time.Time `json:"at"`
}

// AuditPage is the response of GET /api/v1/audit. Entries are oldest first.
// Total counts every entry matching the query before paging. NextCursor is
// empty on the last page.
type AuditPage struct {
	Entries    []AuditRecord `json:"entries"`
	Total      int           `json:"total"`
	NextCursor string        `json:"nextCursor,omitempty"`

	// Truncated reports that the buffer trimmed at least one older record, so
	// this page is not the full audit history. The flag describes the buffer
	// as a whole. A filtered page may still contain every record for its actor
	// while Truncated is true.
	Truncated bool `json:"truncated"`
	// Dropped counts the records the buffer trimmed so far. It tells a consumer
	// how much history is missing from the snapshot. It stays absent until the
	// first trim, which keeps the response unchanged for a fresh process.
	Dropped uint64 `json:"dropped,omitempty"`
}

// Snapshot is the read-only projection served by /api/v1/snapshot. It is a
// transport-owned shape built from domain values, never a live store object.
type Snapshot struct {
	LiveWatches       []domain.Watch              `json:"liveWatches"`
	BlockerIncidents  []domain.BlockerIncident    `json:"blockerIncidents"`
	BoundOrchestrator *domain.OrchestratorBinding `json:"boundOrchestrator,omitempty"`
	Escalations       []string                    `json:"escalations"`
	PointStats        PointStats                  `json:"pointStats"`
	Metrics           json.RawMessage             `json:"metrics"`
	LastCycle         LastCycle                   `json:"lastCycle"`
	LoadErrors        []string                    `json:"loadErrors"`

	// RecentWatches and HourlyStats are optional. A provider that does not
	// implement HistorySource leaves both fields out of the JSON response, so
	// an older client sees the same shape as before they were added.
	RecentWatches []WatchHistory `json:"recentWatches,omitempty"`
	HourlyStats   *HourlyStats   `json:"hourlyStats,omitempty"`

	// RecentWatchesTruncated is true when the provider returned more than
	// maxRecentWatches completed Watches and the transport kept only the newest
	// ones. It stays absent when nothing was cut, so the flag is part of the
	// optional history block rather than a field on every snapshot.
	RecentWatchesTruncated bool `json:"recentWatchesTruncated,omitempty"`
}

// WatchOutcome groups a completed Watch for the history list. It stays a string
// so the OpenAPI enum does not change when the domain gains new verdicts.
type WatchOutcome string

const (
	OutcomeMerged    WatchOutcome = "merged"
	OutcomeClosed    WatchOutcome = "closed"
	OutcomeEscalated WatchOutcome = "escalated"
)

// WatchHistory is one completed Watch projected for a future UI. It is a
// transport-owned shape and never a live domain.Watch, so the wire contract
// stays stable when the domain struct grows.
type WatchHistory struct {
	ID          string       `json:"id"`
	Repo        string       `json:"repo"`
	PR          int          `json:"pr"`
	HeadSHA     string       `json:"headSHA"`
	Outcome     WatchOutcome `json:"outcome"`
	ClosedAt    time.Time    `json:"closedAt"`
	CloseReason string       `json:"closeReason,omitempty"`
}

// HourlyStats is the last hour's activity. Points come from the CI provider's
// budget, merges from completed Watches and blockers from the active incidents.
type HourlyStats struct {
	Points   int `json:"points"`
	Merges   int `json:"merges"`
	Blockers int `json:"blockers"`
}

// SnapshotHistory is the optional history block added to a Snapshot.
type SnapshotHistory struct {
	RecentWatches []WatchHistory `json:"recentWatches"`
	HourlyStats   HourlyStats    `json:"hourlyStats"`
}

// HistorySource is an optional port layered on StateProvider. The transport
// never opens SQLite itself. The composition root decides whether history is
// available and implements this method on the value it passes as Provider.
// RecentWatches must be newest first by ClosedAt; the transport keeps the first
// maxRecentWatches entries and sets RecentWatchesTruncated when it drops any.
// A failed read becomes a LoadErrors entry and the snapshot still returns 200,
// so a UI that does not understand history keeps working.
type HistorySource interface {
	SnapshotHistory(ctx context.Context) (SnapshotHistory, error)
}

// StateProvider supplies the projection. The transport only calls Snapshot.
type StateProvider interface {
	Snapshot(ctx context.Context) (Snapshot, error)
}

// Decision is one human command. ObligationID and Note are optional; Actor is
// recorded in the journal by the sink, not interpreted by the transport.
// Contract names the wire contract version; an empty value keeps the original
// pre-versioning shape valid.
type Decision struct {
	Command      string `json:"command"`
	WatchID      string `json:"watchID"`
	ObligationID string `json:"obligationID,omitempty"`
	Note         string `json:"note,omitempty"`
	Actor        string `json:"actor,omitempty"`
	Contract     string `json:"contract,omitempty"`
	// AppKey identifies the application that sent the command. When set, the
	// server checks that the key is known and that Actor is one of its
	// permitted actors. An empty value keeps the local TUI path unchanged.
	AppKey string `json:"appKey,omitempty"`
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

// DecisionSink applies one command. Idempotency is owned by Server, so the
// sink sees a given decision at most once per process lifetime.
type DecisionSink interface {
	Apply(ctx context.Context, d Decision) (DecisionResult, error)
}

// AppCredentials binds an application key to the actors that application may
// answer for. The composition root supplies the map; this transport never
// issues keys, so T3.275 stays dropped. An app that declares a key can decide
// only for an actor in that key's list.
type AppCredentials map[string][]string

// permits reports whether appKey is known and lists actor. An empty or unknown
// key grants no permission, so a client cannot widen its rights by guessing.
func (c AppCredentials) permits(appKey, actor string) bool {
	if appKey == "" {
		return false
	}
	for _, permitted := range c[appKey] {
		if permitted == actor {
			return true
		}
	}
	return false
}

// Config is the set of ports and facts the composition root hands to Start.
type Config struct {
	Addr          string
	StateDir      string
	Version       string
	ProjectKey    string
	IsPollerOwner bool
	StartedAt     time.Time
	// Token is optional. An empty value generates a random bearer token.
	Token    string
	Provider StateProvider
	Sink     DecisionSink
	Events   *EventBus
	// Credentials lists the permitted actors for each known application key.
	// A nil map means no external app is configured and appKey is ignored.
	Credentials AppCredentials
	// ExtraRoutes mounts additional handlers on this listener, behind the same
	// bearer gateway. The composition root uses it to share one HTTP server
	// with another ingress. A nil map leaves the route table unchanged, so no
	// extra path is exposed by default.
	ExtraRoutes map[string]http.Handler
}

// connectionFile is written next to the state store with mode 0600. The extra
// pid and version fields let a reader tell a live endpoint from a stale one.
type connectionFile struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Token   string `json:"token"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
}

// Event is one SSE frame. ID is assigned by the bus and doubles as the
// Last-Event-ID a client sends on reconnect.
type Event struct {
	ID      uint64          `json:"id"`
	Type    string          `json:"type"`
	At      time.Time       `json:"at"`
	WatchID string          `json:"watchID,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// EventName is the discriminated name of a typed SSE event. The set is closed:
// the three patch names follow the domain objects the stream describes, and
// the snapshot name carries the full state those patches extend. A name from
// outside this set cannot be published through PublishTyped or decoded into a
// TypedEvent, so a subscriber never has to guess what a frame means.
type EventName string

const (
	EventSnapshot        EventName = "snapshot"
	EventWatchPatch      EventName = "watch.patch"
	EventObligationPatch EventName = "obligation.patch"
	EventVerdictPatch    EventName = "verdict.patch"
)

var validEventNames = map[EventName]bool{
	EventSnapshot:        true,
	EventWatchPatch:      true,
	EventObligationPatch: true,
	EventVerdictPatch:    true,
}

// PatchOp is one RFC 6902 JSON Patch operation. Value is kept raw so the
// transport does not reinterpret a JSON literal before the subscriber does.
type PatchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

// PatchPayload is the typed delta between two snapshots. BaseID names the
// snapshot event this patch extends, so a subscriber can tell a stale chain
// from a current one without reading the whole snapshot again.
type PatchPayload struct {
	Contract string    `json:"contract"`
	BaseID   uint64    `json:"baseID"`
	Ops      []PatchOp `json:"ops"`
}

// SnapshotPayload is the typed full state. A producer sends one of these at
// the start of a stream and at most once per maxPatchesPerSnapshot patches.
type SnapshotPayload struct {
	Contract string   `json:"contract"`
	Snapshot Snapshot `json:"snapshot"`
}

// TypedEvent is the decoded form of an SSE frame. Exactly one of Snapshot and
// Patch is set for a name in the closed set. A subscriber reads this instead
// of parsing Event.Payload by hand.
type TypedEvent struct {
	ID       uint64
	Name     EventName
	Contract string
	WatchID  string
	Snapshot *Snapshot
	Patch    *PatchPayload
}

var validPatchOps = map[string]bool{
	"add": true, "remove": true, "replace": true,
	"move": true, "copy": true, "test": true,
}

// Typed stream errors. Callers use errors.Is to tell a contract violation from
// a snapshot the producer still owes the stream.
var (
	ErrUnknownEventName = errors.New("httpapi: unknown event name")
	ErrInvalidPayload   = errors.New("httpapi: invalid event payload")
	ErrSnapshotDue      = errors.New("httpapi: snapshot due before more patches")
)

func validateEventName(name EventName) error {
	if !validEventNames[name] {
		return fmt.Errorf("%w: %q", ErrUnknownEventName, name)
	}
	return nil
}

// validatePayload checks the shape of a typed payload before it enters the
// bus. A snapshot must name the current contract; a patch must name it, carry
// at least one operation, and use a valid operation and a non-empty path.
func validatePayload(name EventName, raw []byte) error {
	if err := validateEventName(name); err != nil {
		return err
	}
	if name == EventSnapshot {
		var p SnapshotPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("%w: snapshot: %v", ErrInvalidPayload, err)
		}
		if p.Contract != EventContract {
			return fmt.Errorf("%w: snapshot contract %q", ErrInvalidPayload, p.Contract)
		}
		return nil
	}
	var p PatchPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("%w: patch: %v", ErrInvalidPayload, err)
	}
	if p.Contract != EventContract {
		return fmt.Errorf("%w: patch contract %q", ErrInvalidPayload, p.Contract)
	}
	if len(p.Ops) == 0 {
		return fmt.Errorf("%w: patch has no operations", ErrInvalidPayload)
	}
	for _, op := range p.Ops {
		if !validPatchOps[op.Op] {
			return fmt.Errorf("%w: patch op %q", ErrInvalidPayload, op.Op)
		}
		if strings.TrimSpace(op.Path) == "" {
			return fmt.Errorf("%w: patch op %q has no path", ErrInvalidPayload, op.Op)
		}
	}
	return nil
}

// DecodeTypedEvent turns an SSE frame into its typed form. A name outside the
// closed set and a payload of the wrong shape are both explicit errors, so a
// subscriber can reject a bad frame instead of quietly ignoring it.
func DecodeTypedEvent(e Event) (TypedEvent, error) {
	name := EventName(e.Type)
	if err := validateEventName(name); err != nil {
		return TypedEvent{}, err
	}
	if err := validatePayload(name, e.Payload); err != nil {
		return TypedEvent{}, err
	}
	out := TypedEvent{ID: e.ID, Name: name, WatchID: e.WatchID}
	if name == EventSnapshot {
		var p SnapshotPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return TypedEvent{}, fmt.Errorf("%w: snapshot: %v", ErrInvalidPayload, err)
		}
		out.Contract = p.Contract
		out.Snapshot = &p.Snapshot
		return out, nil
	}
	var p PatchPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return TypedEvent{}, fmt.Errorf("%w: patch: %v", ErrInvalidPayload, err)
	}
	out.Contract = p.Contract
	out.Patch = &p
	return out, nil
}

// EventBus keeps a bounded ring of recent events and fans live events out to
// SSE subscribers. Slow subscribers drop live frames but can replay them from
// the ring with Last-Event-ID.
type EventBus struct {
	mu       sync.Mutex
	next     uint64
	ring     []Event
	capacity int
	subs     map[chan Event]struct{}
	closed   bool

	// The typed stream keeps a snapshot anchor. Patches count against the cap
	// until the next full snapshot resets them, and a patch may name the anchor
	// in its BaseID so a subscriber can spot a stale chain.
	patchesSinceSnapshot uint64
	lastSnapshotID       uint64
}

// NewEventBus builds a bus holding at most capacity events. A non-positive
// capacity selects the default, so no caller can build an unbounded buffer.
func NewEventBus(capacity int) *EventBus {
	if capacity <= 0 {
		capacity = defaultEventCapacity
	}
	return &EventBus{capacity: capacity, subs: make(map[chan Event]struct{})}
}

// Publish assigns an id and a timestamp, then stores the event and offers it
// to every live subscriber without blocking on a slow one. The legacy path
// accepts any name, so existing publishers keep their frames unchanged.
func (b *EventBus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.publishLocked(e)
}

// publishLocked stores and fans out one frame. The caller must hold b.mu.
func (b *EventBus) publishLocked(e Event) {
	if b.closed {
		return
	}
	b.next++
	e.ID = b.next
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	b.ring = append(b.ring, e)
	if len(b.ring) > b.capacity {
		copy(b.ring, b.ring[len(b.ring)-b.capacity:])
		b.ring = b.ring[:b.capacity]
	}
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// PublishTyped validates a typed name and payload, enforces the snapshot
// cadence, and then publishes an ordinary frame. The wire shape stays the
// same, so existing SSE subscribers and the /api/v1/ contract do not change.
// A payload of *PatchPayload gets BaseID filled from the current snapshot so
// a subscriber can always tell which full state a delta extends.
func (b *EventBus) PublishTyped(name EventName, payload any) error {
	if err := validateEventName(name); err != nil {
		return err
	}
	if patch, ok := payload.(*PatchPayload); ok {
		copyPatch := *patch
		copyPatch.BaseID = b.LastSnapshotID()
		payload = &copyPatch
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%w: marshal: %v", ErrInvalidPayload, err)
	}
	if err := validatePayload(name, raw); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("httpapi: event bus is closed")
	}
	if name == EventSnapshot {
		b.patchesSinceSnapshot = 0
		b.lastSnapshotID = b.next + 1
	} else {
		if b.patchesSinceSnapshot >= maxPatchesPerSnapshot {
			return fmt.Errorf("%w: %d patches since snapshot", ErrSnapshotDue, b.patchesSinceSnapshot)
		}
		b.patchesSinceSnapshot++
	}
	b.publishLocked(Event{Type: string(name), Payload: raw})
	return nil
}

// LastSnapshotID is the id of the most recent full snapshot on this bus. Zero
// means no typed snapshot has been published yet. A producer passes it as the
// BaseID of a patch, or lets PublishTyped fill it in.
func (b *EventBus) LastSnapshotID() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastSnapshotID
}

// Subscribe returns a live event channel and the function that removes it.
// The returned function is safe to call more than once.
func (b *EventBus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subscriberBuffer)
	b.mu.Lock()
	if b.closed {
		close(ch)
		b.mu.Unlock()
		return ch, func() {}
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			if _, ok := b.subs[ch]; ok {
				delete(b.subs, ch)
				close(ch)
			}
			b.mu.Unlock()
		})
	}
}

// Since returns buffered events with an id greater than lastID, in order.
func (b *EventBus) Since(lastID uint64) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Event, 0, len(b.ring))
	for _, e := range b.ring {
		if e.ID > lastID {
			out = append(out, e)
		}
	}
	return out
}

// Last returns the most recent n buffered events, in order.
func (b *EventBus) Last(n int) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > len(b.ring) {
		n = len(b.ring)
	}
	if n <= 0 {
		return nil
	}
	out := make([]Event, n)
	copy(out, b.ring[len(b.ring)-n:])
	return out
}

func (b *EventBus) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for ch := range b.subs {
		close(ch)
		delete(b.subs, ch)
	}
}

// opsState holds the in-process counters behind /version, /metrics and
// /audit. Scrapes and decisions arrive on different goroutines, so every
// field is guarded by mu.
type opsState struct {
	mu              sync.Mutex
	wakesTotal      uint64
	wakeLatencySum  float64
	wakeLatencyMax  float64
	wakeLatencyLast float64
	audit           []AuditRecord
	// auditDropped counts the records the trim removes once audit reaches
	// maxAuditRecords. It survives the trim, so auditView can tell a consumer
	// that older history is missing from the snapshot.
	auditDropped uint64
}

// observeWake records one wake latency in milliseconds. A negative duration
// is clamped to zero so a broken clock cannot produce a negative metric.
func (o *opsState) observeWake(latency time.Duration) {
	ms := float64(latency) / float64(time.Millisecond)
	if ms < 0 {
		ms = 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.wakesTotal++
	o.wakeLatencySum += ms
	if ms > o.wakeLatencyMax {
		o.wakeLatencyMax = ms
	}
	o.wakeLatencyLast = ms
}

// metricsView returns a copy of the counters safe to encode while new wakes
// keep arriving.
func (o *opsState) metricsView() OpsMetrics {
	o.mu.Lock()
	defer o.mu.Unlock()
	m := OpsMetrics{
		WakesTotal:        o.wakesTotal,
		WakeLatencyMaxMs:  o.wakeLatencyMax,
		WakeLatencyLastMs: o.wakeLatencyLast,
	}
	if o.wakesTotal > 0 {
		m.WakeLatencyMs = o.wakeLatencySum / float64(o.wakesTotal)
	}
	return m
}

// recordAudit appends one command to the bounded audit list. When the list is
// full it drops the oldest records and counts them, so a later auditView can
// report that the snapshot is incomplete.
func (o *opsState) recordAudit(rec AuditRecord) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.audit = append(o.audit, rec)
	if overflow := len(o.audit) - maxAuditRecords; overflow > 0 {
		o.audit = append([]AuditRecord(nil), o.audit[overflow:]...)
		o.auditDropped += uint64(overflow)
	}
}

// auditView returns one page of recorded commands, optionally filtered by
// actor. A cursor is an opaque offset into the matching entries, so a caller
// can walk back to the oldest record instead of losing it at a fixed cap.
func (o *opsState) auditView(actor, cursor string, limit int) (AuditPage, error) {
	offset, err := decodeAuditCursor(cursor)
	if err != nil {
		return AuditPage{}, err
	}
	if limit <= 0 {
		limit = defaultAuditPageSize
	}
	if limit > maxAuditPageSize {
		limit = maxAuditPageSize
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	entries := make([]AuditRecord, 0, len(o.audit))
	for _, rec := range o.audit {
		if actor != "" && rec.Actor != actor {
			continue
		}
		entries = append(entries, rec)
	}
	total := len(entries)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total || end < offset {
		end = total
	}
	next := ""
	if end < total {
		next = encodeAuditCursor(end)
	}
	return AuditPage{
		Entries:    entries[offset:end],
		Total:      total,
		NextCursor: next,
		Truncated:  o.auditDropped > 0,
		Dropped:    o.auditDropped,
	}, nil
}

// encodeAuditCursor and decodeAuditCursor use the same opaque offset format as
// the persistent audit projection in internal/infra/server. A client treats
// the value as a token and passes it back unchanged.
func encodeAuditCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func decodeAuditCursor(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, fmt.Errorf("invalid audit cursor: %v", err)
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid audit cursor: %q", s)
	}
	return offset, nil
}

// Server owns the listener, the bearer token and the connection file. Close
// shuts the listener down and removes the connection file only when it still
// carries this process' pid.
type Server struct {
	cfg        Config
	token      string
	host       string
	port       int
	pid        int
	listener   net.Listener
	httpServer *http.Server
	events     *EventBus
	ops        opsState

	decisions map[string]DecisionResult
	decMu     sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

// Start opens a loopback listener, publishes the endpoint in connection.json
// and begins serving. The caller owns the returned server and must Close it.
func Start(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.StateDir) == "" {
		return nil, errors.New("httpapi: empty state dir")
	}
	if cfg.Provider == nil {
		return nil, errors.New("httpapi: nil state provider")
	}
	if cfg.Sink == nil {
		return nil, errors.New("httpapi: nil decision sink")
	}

	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		var err error
		token, err = randomToken()
		if err != nil {
			return nil, err
		}
	}

	addr := strings.TrimSpace(cfg.Addr)
	if addr == "" {
		addr = loopbackAddr
	}
	listenAddr, err := loopbackListenAddr(addr)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("httpapi: listen %s: %w", listenAddr, err)
	}
	tcp, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, fmt.Errorf("httpapi: unexpected listener address %T", listener.Addr())
	}
	host := tcp.IP.String()
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}

	events := cfg.Events
	if events == nil {
		events = NewEventBus(0)
	}
	s := newServer(cfg, token, host, tcp.Port, events)
	s.listener = listener
	if err := s.writeConnection(); err != nil {
		_ = listener.Close()
		return nil, err
	}
	s.httpServer = &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	go func() {
		if err := s.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			observe.ActorError("httpapi-serve", "", err)
		}
	}()
	return s, nil
}

func newServer(cfg Config, token, host string, port int, events *EventBus) *Server {
	if events == nil {
		events = NewEventBus(0)
	}
	return &Server{
		cfg:       cfg,
		token:     token,
		host:      host,
		port:      port,
		pid:       os.Getpid(),
		events:    events,
		decisions: make(map[string]DecisionResult),
	}
}

// ObserveWake records the latency of one delivered wake. The composition root
// calls this when a wake reaches a subscriber; GET /api/v1/metrics reports the
// running summary. It is safe to call concurrently with scrapes.
func (s *Server) ObserveWake(latency time.Duration) {
	s.ops.observeWake(latency)
}

// startedAt returns the configured start time, falling back to now when the
// caller leaves it unset.
func (s *Server) startedAt() time.Time {
	if s.cfg.StartedAt.IsZero() {
		return time.Now()
	}
	return s.cfg.StartedAt
}

// uptimeSec is whole seconds since start, never negative.
func (s *Server) uptimeSec() int64 {
	uptime := int64(time.Since(s.startedAt()).Seconds())
	if uptime < 0 {
		return 0
	}
	return uptime
}

// loopbackListenAddr keeps the port from addr but always binds 127.0.0.1.
func loopbackListenAddr(addr string) (string, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("httpapi: bad address %q: %w", addr, err)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", fmt.Errorf("httpapi: bad port in %q: %w", addr, err)
	}
	return net.JoinHostPort("127.0.0.1", port), nil
}

func randomToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("httpapi: token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// writeConnection writes the endpoint atomically so a reader never observes a
// half-written file. CreateTemp already uses 0600 on Unix.
func (s *Server) writeConnection() error {
	payload, err := json.Marshal(connectionFile{
		Host: s.host, Port: s.port, Token: s.token, PID: s.pid, Version: s.cfg.Version,
	})
	if err != nil {
		return fmt.Errorf("httpapi: encode connection: %w", err)
	}
	tmp, err := os.CreateTemp(s.cfg.StateDir, ".connection-*.tmp")
	if err != nil {
		return fmt.Errorf("httpapi: temp connection: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("httpapi: write connection: %w", err)
	}
	if err := tmp.Chmod(connectionPerm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("httpapi: chmod connection: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("httpapi: close connection: %w", err)
	}
	if err := os.Rename(name, filepath.Join(s.cfg.StateDir, connectionFileName)); err != nil {
		return fmt.Errorf("httpapi: install connection: %w", err)
	}
	return nil
}

// Close stops serving and removes the connection file only when it still
// points at this process. A file written by a newer owner is left untouched.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		if s.httpServer != nil {
			ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			if err := s.httpServer.Shutdown(ctx); err != nil {
				s.closeErr = err
				_ = s.httpServer.Close()
			}
		} else if s.listener != nil {
			_ = s.listener.Close()
		}
		s.events.close()
		s.removeConnectionIfMine()
	})
	return s.closeErr
}

func (s *Server) removeConnectionIfMine() {
	path := filepath.Join(s.cfg.StateDir, connectionFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var current connectionFile
	if json.Unmarshal(raw, &current) != nil || current.PID != s.pid {
		return
	}
	_ = os.Remove(path)
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)
	mux.HandleFunc("GET /api/v1/metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/v1/audit", s.handleAudit)
	mux.HandleFunc("GET /api/v1/snapshot", s.handleSnapshot)
	mux.HandleFunc("GET /api/v1/events", s.handleEvents)
	mux.Handle("POST /api/v1/human/decide", s.sameOriginJSON(http.HandlerFunc(s.handleDecide)))
	for path, h := range s.cfg.ExtraRoutes {
		mux.Handle(path, h)
	}
	return s.bearer(mux)
}

func (s *Server) bearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Bearer " + s.token
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "неверный bearer-токен")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOriginJSON guards a state-changing JSON POST against cross-site
// requests. A browser must send an Origin matching the loopback listener;
// command-line callers that send no Origin are allowed.
func (s *Server) sameOriginJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := strings.ToLower(r.Header.Get("Content-Type")); !strings.HasPrefix(ct, "application/json") {
			writeError(w, http.StatusUnsupportedMediaType, string(domain.CodeInvalidInput), "нужен Content-Type: application/json")
			return
		}
		if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && !s.sameOrigin(origin) {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "Origin не совпадает с адресом сервера")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sameOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" {
		return false
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	if port != strconv.Itoa(s.port) {
		return false
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type errorBody struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError sends a transport-level error. A code that also exists in the
// domain registry is passed as string(domain.CodeX), so its meaning has one
// literal. The remaining codes are httpapi-only strings: this server owns its
// transport contract and the status is supplied explicitly at each call site,
// so these are an explicit exception rather than a second error-code type.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Code: code, Message: message})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	uptime := s.uptimeSec()
	body := struct {
		OK            bool     `json:"ok"`
		Version       string   `json:"version"`
		PID           int      `json:"pid"`
		ProjectKey    string   `json:"projectKey"`
		IsPollerOwner bool     `json:"isPollerOwner"`
		UptimeSec     int64    `json:"uptimeSec"`
		LoadErrors    []string `json:"loadErrors"`
	}{
		OK: true, Version: s.cfg.Version, PID: s.pid,
		ProjectKey: s.cfg.ProjectKey, IsPollerOwner: s.cfg.IsPollerOwner,
		UptimeSec: uptime, LoadErrors: []string{},
	}
	snap, err := s.cfg.Provider.Snapshot(r.Context())
	if err != nil {
		body.OK = false
		body.LoadErrors = []string{err.Error()}
	} else {
		body.LoadErrors = append(body.LoadErrors, snap.LoadErrors...)
	}
	writeJSON(w, http.StatusOK, body)
}

// handleVersion serves build facts without touching the provider, so it keeps
// answering even when the store read that backs /health is failing.
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, VersionInfo{
		OK: true, Version: s.cfg.Version, PID: s.pid, ProjectKey: s.cfg.ProjectKey,
		StartedAt: s.startedAt(), UptimeSec: s.uptimeSec(),
	})
}

// handleMetrics serves operational counters. ActiveTasks is derived from the
// live Watches in the snapshot so it always matches the number of unfinished
// runs at scrape time.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	snap, err := s.cfg.Provider.Snapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "METRICS_FAILED", err.Error())
		return
	}
	m := s.ops.metricsView()
	m.OK = true
	m.ActiveTasks = len(snap.LiveWatches)
	m.UptimeSec = s.uptimeSec()
	writeJSON(w, http.StatusOK, m)
}

// handleAudit serves the commands recorded by this transport. An optional
// actor query parameter narrows the list. A cursor walks through older pages
// so a caller can reach the first recorded command. Actor and ingress are
// always present on every entry.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	actor := ""
	cursor := ""
	limit := 0
	if r.URL != nil {
		q := r.URL.Query()
		actor = strings.TrimSpace(q.Get("actor"))
		cursor = strings.TrimSpace(q.Get("cursor"))
		if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				writeError(w, http.StatusBadRequest, "INVALID_AUDIT_LIMIT", "limit must be a positive integer")
				return
			}
			limit = parsed
		}
	}
	page, err := s.ops.auditView(actor, cursor, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_AUDIT_CURSOR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	snap, err := s.cfg.Provider.Snapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SNAPSHOT_FAILED", err.Error())
		return
	}
	if snap.LiveWatches == nil {
		snap.LiveWatches = []domain.Watch{}
	}
	if snap.BlockerIncidents == nil {
		snap.BlockerIncidents = []domain.BlockerIncident{}
	}
	if snap.Escalations == nil {
		snap.Escalations = []string{}
	}
	if snap.LoadErrors == nil {
		snap.LoadErrors = []string{}
	}
	if history, ok := s.cfg.Provider.(HistorySource); ok {
		h, err := history.SnapshotHistory(r.Context())
		if err != nil {
			snap.LoadErrors = append(snap.LoadErrors, "history: "+err.Error())
		} else {
			// A provider may already have filled either field from its own
			// store read. Keep that value and use the port only for the gaps.
			if snap.RecentWatches == nil {
				snap.RecentWatches = h.RecentWatches
				if snap.RecentWatches == nil {
					snap.RecentWatches = []WatchHistory{}
				}
			}
			if snap.HourlyStats == nil {
				stats := h.HourlyStats
				snap.HourlyStats = &stats
			}
		}
	}
	if len(snap.RecentWatches) > maxRecentWatches {
		snap.RecentWatches = snap.RecentWatches[:maxRecentWatches]
		snap.RecentWatchesTruncated = true
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STREAM_UNSUPPORTED", "writer не поддерживает поток")
		return
	}
	ch, unsubscribe := s.events.Subscribe()
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	last := uint64(0)
	for _, e := range s.replay(r) {
		writeSSE(w, flusher, e)
		last = e.ID
	}
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			if e.ID <= last {
				continue
			}
			writeSSE(w, flusher, e)
			last = e.ID
		case <-ticker.C:
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) replay(r *http.Request) []Event {
	if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
		if id, err := strconv.ParseUint(raw, 10, 64); err == nil {
			return s.events.Since(id)
		}
	}
	raw := strings.TrimSpace(r.URL.Query().Get("replay"))
	if raw == "" {
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return nil
	}
	if n > maxReplay {
		n = maxReplay
	}
	return s.events.Last(n)
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, e Event) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	name := sanitizeEventName(e.Type)
	_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, name, data)
	flusher.Flush()
}

func sanitizeEventName(name string) string {
	if name == "" {
		return "message"
	}
	for i, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-'
		if !ok {
			if i == 0 {
				return "message"
			}
			return name[:i]
		}
	}
	return name
}

var validCommands = map[string]bool{
	"approve": true, "reject": true, "context": true, "rework": true,
	"pause": true, "resume": true, "unblock": true, "cancel": true,
	"force_retry_merge": true,
}

func (s *Server) handleDecide(w http.ResponseWriter, r *http.Request) {
	var d Decision
	body := http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := json.NewDecoder(body).Decode(&d); err != nil {
		writeError(w, http.StatusBadRequest, string(domain.CodeInvalidInput), "неверное тело запроса")
		return
	}
	d.Command = strings.ToLower(strings.TrimSpace(d.Command))
	d.WatchID = strings.TrimSpace(d.WatchID)
	d.ObligationID = strings.TrimSpace(d.ObligationID)
	if !validCommands[d.Command] {
		writeError(w, http.StatusBadRequest, string(domain.CodeInvalidInput), "неизвестная команда "+d.Command)
		return
	}
	if d.WatchID == "" {
		writeError(w, http.StatusBadRequest, string(domain.CodeInvalidInput), "пустое поле watchID")
		return
	}
	if d.Contract != "" && d.Contract != HumanDecideContract {
		writeError(w, http.StatusBadRequest, "UNSUPPORTED_CONTRACT", "неподдерживаемая версия контракта "+d.Contract)
		return
	}
	// An application may answer only for its own actors. A local caller sends
	// no appKey and keeps the TUI path; a declared key must be known and carry
	// this actor in its permit list. Without a credential registry there is
	// nothing to check, so the key is ignored.
	if d.AppKey != "" && s.cfg.Credentials != nil && !s.cfg.Credentials.permits(d.AppKey, d.Actor) {
		writeError(w, http.StatusForbidden, "ACTOR_NOT_PERMITTED", "приложение не вправе отвечать за actor "+d.Actor)
		return
	}

	key := strings.Join([]string{d.Command, d.WatchID, d.ObligationID, d.Note}, "\x00")
	s.decMu.Lock()
	if previous, ok := s.decisions[key]; ok {
		previous.Applied = false
		s.decMu.Unlock()
		writeJSON(w, http.StatusOK, previous)
		return
	}
	result, err := s.cfg.Sink.Apply(r.Context(), d)
	if err != nil {
		s.decMu.Unlock()
		writeError(w, http.StatusInternalServerError, "DECISION_FAILED", err.Error())
		return
	}
	// The response always echoes the accepted request. A sink is free to
	// return only its own fields, so the transport fills the identity here.
	if result.Command == "" {
		result.Command = d.Command
	}
	if result.WatchID == "" {
		result.WatchID = d.WatchID
	}
	result.OK = true
	result.Applied = true
	if result.DecisionID == "" {
		result.DecisionID = key
	}
	result.Contract = HumanDecideContract
	s.decisions[key] = result
	s.decMu.Unlock()
	s.ops.recordAudit(AuditRecord{
		Actor: d.Actor, Ingress: ingressHTTPAPI, Command: result.Command,
		WatchID: result.WatchID, DecisionID: result.DecisionID, At: time.Now().UTC(),
	})
	writeJSON(w, http.StatusOK, result)
}

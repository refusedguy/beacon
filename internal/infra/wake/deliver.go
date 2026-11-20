// Package wake delivers obligations from authoritative state, using wake
// events only as hints (ADR 0080, T3.208).
//
// Events are hints, state is truth. An event carries only an id and a seq; it
// never carries an obligation. Obligations enter a Deliverer exclusively
// through a full List of authoritative state, so a missed event costs a
// wake-up, never an obligation: the next full read recovers it. Events are
// deduplicated by id and coalesced into one dirty signal of capacity one. A
// late subscriber gets no replay of past events (Pi Chord); it gets the
// current state from a full list instead.
//
// Cursors are process-local. They live only in Deliverer memory and are never
// persisted, so a restarted process always begins with a full list. A 410 Gone
// or cursor_expired signal from a watch discards the dead cursor and triggers
// an immediate re-list; the deliverer never depends on automatic reconnect.
package wake

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// CursorExpired is the source code reported when a watch cursor is no longer
// valid. Together with HTTP 410 Gone it is a re-list signal.
const CursorExpired = "cursor_expired"

// httpStatusGone is the HTTP status that means a resource and its cursor are
// gone. It is kept local so this package does not depend on net/http.
const httpStatusGone = 410

// GoneError reports that a watch cursor is dead. It models both HTTP 410 Gone
// and a source that reports cursor_expired. The deliverer answers it with a
// full re-list instead of retrying the dead cursor.
type GoneError struct {
	Status int
	Code   string
}

func (e *GoneError) Error() string {
	return fmt.Sprintf("wake: cursor gone (status=%d code=%q)", e.Status, e.Code)
}

// IsGone reports whether err carries a 410 Gone or cursor_expired signal.
func IsGone(err error) bool {
	var g *GoneError
	if !errors.As(err, &g) {
		return false
	}
	return g.Status == httpStatusGone || g.Code == CursorExpired
}

// Event is a wake hint. It carries only an id, used for deduplication, and a
// seq, used for ordering. It never carries an obligation or state, and it is
// never replayed to a late subscriber. State comes from List, not from here.
type Event struct {
	ID  string
	Seq uint64
}

// Obligation is one durable unit of work discovered in authoritative state.
type Obligation struct {
	ID       string
	Revision uint64
}

// Snapshot is one full read of authoritative state. Revision orders snapshots
// so an older read can never overwrite a newer one. Cursor is a process-local
// watch position; it dies with the process and is never persisted.
type Snapshot struct {
	Revision    uint64
	Cursor      string
	Obligations []Obligation
}

// StateSource is the authoritative reader behind a Deliverer. List is a full
// read and the only way obligations enter the deliverer. Watch waits for the
// next hint from a process-local cursor and may fail with a GoneError.
type StateSource interface {
	List(ctx context.Context) (Snapshot, error)
	Watch(ctx context.Context, cursor string) (Event, error)
}

// Deliverer reconciles authoritative state into a local obligation set. It
// holds an event-id dedup index, a high-water seq, and a coalescing dirty bit
// of capacity one. Every field, including the cursor, is in memory: a restart
// starts with no state and no cursor, hence with a full list.
type Deliverer struct {
	source StateSource
	jitter func() time.Duration

	mu          sync.Mutex
	byID        map[string]Obligation
	seen        map[string]struct{}
	lastSeq     uint64
	hasSeq      bool
	revision    uint64
	hasRevision bool
	cursor      string
	dirty       bool
	relists     int
	goneRelists int
}

// NewDeliverer builds a Deliverer over source. jitter supplies the delay added
// before each periodic full read; a nil jitter means no delay. The deliverer
// starts with no state and no cursor, so its first Reconcile is always a full
// list.
func NewDeliverer(source StateSource, jitter func() time.Duration) *Deliverer {
	return &Deliverer{
		source: source,
		jitter: jitter,
		byID:   make(map[string]Obligation),
		seen:   make(map[string]struct{}),
	}
}

// Apply records a wake hint. It is idempotent by event id: a repeated id is a
// no-op and never changes state. An event whose seq is not newer than the
// high-water mark is discarded. A fresh event only sets the coalescing dirty
// bit; it never changes obligations, because obligations come from List. It
// reports whether the event was accepted.
func (d *Deliverer) Apply(ev Event) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ev.ID == "" {
		return false
	}
	if _, ok := d.seen[ev.ID]; ok {
		return false
	}
	d.seen[ev.ID] = struct{}{}
	if d.hasSeq && ev.Seq <= d.lastSeq {
		return false
	}
	if !d.hasSeq || ev.Seq > d.lastSeq {
		d.lastSeq = ev.Seq
		d.hasSeq = true
	}
	d.dirty = true
	return true
}

// Reconcile performs one full List and makes its obligations the current
// state. It is unconditional: even with no events, a periodic reconcile
// recovers an obligation whose event was lost. A snapshot whose revision is
// not newer than the current one is discarded, so a late or duplicated read
// cannot roll state back. mu is held across the read to keep one writer at a
// time. It returns the number of obligations now held and whether the snapshot
// was applied.
func (d *Deliverer) Reconcile(ctx context.Context) (int, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.source == nil {
		return 0, false, errors.New("wake: no state source")
	}
	snap, err := d.source.List(ctx)
	if err != nil {
		return len(d.byID), false, err
	}
	if d.hasRevision && snap.Revision <= d.revision {
		return len(d.byID), false, nil
	}
	d.hasRevision = true
	d.revision = snap.Revision
	d.cursor = snap.Cursor
	d.byID = make(map[string]Obligation, len(snap.Obligations))
	for _, o := range snap.Obligations {
		d.byID[o.ID] = o
	}
	d.dirty = false
	d.relists++
	return len(d.byID), true, nil
}

// WatchOnce waits for one hint from the current process-local cursor and
// applies it. If the source reports 410 Gone or cursor_expired, the dead
// cursor is discarded and an immediate full re-list runs; no automatic
// reconnect is assumed. It reports the event, whether a re-list happened, and
// any error.
func (d *Deliverer) WatchOnce(ctx context.Context) (Event, bool, error) {
	if d.source == nil {
		return Event{}, false, errors.New("wake: no state source")
	}
	d.mu.Lock()
	cursor := d.cursor
	d.mu.Unlock()

	ev, err := d.source.Watch(ctx, cursor)
	if err != nil {
		if IsGone(err) {
			_, _, relistErr := d.Reconcile(ctx)
			if relistErr == nil {
				d.mu.Lock()
				d.goneRelists++
				d.mu.Unlock()
			}
			return Event{}, true, relistErr
		}
		return Event{}, false, err
	}
	d.Apply(ev)
	return ev, false, nil
}

// Run drives periodic full reads until ctx is cancelled. It lists once at
// startup, then on every tick waits the configured jitter and lists again.
// This periodic read is the safety net that makes the deliverer independent
// of any single lost event or missing reconnect.
func (d *Deliverer) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	_, _, _ = d.Reconcile(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if d.jitter != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(d.jitter()):
				}
			}
			_, _, _ = d.Reconcile(ctx)
		}
	}
}

// Obligations returns a copy of the current authoritative obligation set.
func (d *Deliverer) Obligations() []Obligation {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Obligation, 0, len(d.byID))
	for _, o := range d.byID {
		out = append(out, o)
	}
	return out
}

// Has reports whether an obligation id is currently held.
func (d *Deliverer) Has(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.byID[id]
	return ok
}

// State reports the last applied revision, the current process-local cursor,
// and whether an unprocessed hint is pending.
func (d *Deliverer) State() (revision uint64, cursor string, dirty bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.revision, d.cursor, d.dirty
}

// Relists returns how many full reads have been applied.
func (d *Deliverer) Relists() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.relists
}

// GoneRelists returns how many re-lists were triggered by 410/cursor_expired.
func (d *Deliverer) GoneRelists() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.goneRelists
}

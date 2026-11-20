// Package eventlog is an append-only log with a monotonic sequence number.
// It assigns seq values under a lock and stores every event through a Port.
// The package opens no file and knows no database, so the same logic runs
// against an in-memory port in tests and a store adapter in the server.
//
// A seq is never reused and never decreases. It doubles as a replay cursor: a
// reader that has seen seq N resumes with an exclusive cursor of N and receives
// only later events. Every event carries the schema version of its payload, so
// a replay decoder can pick a parser without guessing.
package eventlog

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// SchemaVersion is the payload format stamped on every appended event. A
// replay decoder reads Event.SchemaVersion to select its parser.
const SchemaVersion = 1

var (
	// ErrMissingPort reports construction or use without a persistence port.
	ErrMissingPort = errors.New("eventlog: missing port")
	// ErrInvalidInput reports an empty scope or type, or a negative limit.
	ErrInvalidInput = errors.New("eventlog: invalid input")
)

// Event is one committed record of the log. Seq is assigned by Log and is
// unique across the whole log. Scope and Type select replay filters.
// SchemaVersion is fixed at append time and never rewritten. Payload is copied
// on append, so a caller cannot change a committed event through its slice.
type Event struct {
	Seq           uint64 `json:"seq"`
	Scope         string `json:"scope"`
	Type          string `json:"type"`
	SchemaVersion int    `json:"schema_version"`
	Payload       []byte `json:"payload"`
}

// Query selects a page of events for replay. An empty Scope or Type matches
// every value. After is an exclusive cursor: only events with Seq greater than
// After are returned. Limit caps the page size; zero means no limit.
type Query struct {
	Scope string
	Type  string
	After uint64
	Limit int
}

// Port is the persistence boundary of the log. An implementation stores events
// with the exact Seq values it receives and returns them ordered by Seq. Append
// must be atomic: either every event in the batch is stored or none is.
type Port interface {
	// Append persists events atomically in the order given.
	Append(ctx context.Context, events []Event) error
	// Load returns events matching q, ordered by Seq.
	Load(ctx context.Context, q Query) ([]Event, error)
	// MaxSeq returns the highest Seq stored, or zero for an empty store.
	MaxSeq(ctx context.Context) (uint64, error)
}

// Log assigns monotonic seq values and delegates storage to a Port. The mutex
// serializes seq assignment with the port write, so the order events are stored
// matches the order their seq values were handed out. Concurrent callers can
// never observe a repeated or decreasing seq.
type Log struct {
	port Port

	mu  sync.Mutex
	seq uint64 // highest seq assigned so far
}

// New builds a Log over port. It reads the port's current maximum seq so a
// restarted log continues from the last stored event instead of reusing seq
// values. A nil port or a failing MaxSeq call is refused.
func New(ctx context.Context, port Port) (*Log, error) {
	if port == nil {
		return nil, ErrMissingPort
	}
	if ctx == nil {
		ctx = context.Background()
	}
	max, err := port.MaxSeq(ctx)
	if err != nil {
		return nil, fmt.Errorf("eventlog: read max seq: %w", err)
	}
	return &Log{port: port, seq: max}, nil
}

// Append stamps one event with the next seq and the current schema version,
// then persists it through the port. It returns the stored event. If the port
// fails, the seq is not consumed and the caller gets a wrapped error, so a
// retry writes the same seq and the stored stream stays monotonic.
func (l *Log) Append(ctx context.Context, scope, eventType string, payload []byte) (Event, error) {
	if l == nil || l.port == nil {
		return Event{}, ErrMissingPort
	}
	if scope == "" {
		return Event{}, fmt.Errorf("%w: empty scope", ErrInvalidInput)
	}
	if eventType == "" {
		return Event{}, fmt.Errorf("%w: empty type", ErrInvalidInput)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	event := Event{
		Scope:         scope,
		Type:          eventType,
		SchemaVersion: SchemaVersion,
		Payload:       append([]byte(nil), payload...),
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	event.Seq = l.seq + 1
	if err := l.port.Append(ctx, []Event{event}); err != nil {
		return Event{}, fmt.Errorf("eventlog: append: %w", err)
	}
	l.seq = event.Seq
	return event, nil
}

// MaxSeq returns the highest seq assigned so far. Zero means no event has been
// appended. It reflects successful writes only: a failed append does not move
// the counter.
func (l *Log) MaxSeq() uint64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// Replay returns one page of events matching q through the port. An empty log
// or a cursor past the last seq yields an empty slice.
func (l *Log) Replay(ctx context.Context, q Query) ([]Event, error) {
	if l == nil || l.port == nil {
		return nil, ErrMissingPort
	}
	if q.Limit < 0 {
		return nil, fmt.Errorf("%w: negative limit", ErrInvalidInput)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	events, err := l.port.Load(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("eventlog: replay: %w", err)
	}
	return events, nil
}

// ReadAfter returns every event in scope with a seq greater than cursor, in
// seq order. Cursor zero reads the whole scope.
func (l *Log) ReadAfter(ctx context.Context, scope string, cursor uint64) ([]Event, error) {
	return l.Replay(ctx, Query{Scope: scope, After: cursor})
}

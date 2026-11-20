// Package tasks holds the pure logic that watches task cards. The card monitor
// reads card status and journal events through a Port and emits one change per
// observed status change. All I/O and serialization stay behind that port, so
// this package imports only the standard library (ADR 0005).
package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// EventTypeStatusChanged marks a change event produced by the monitor.
const EventTypeStatusChanged = "card.status_changed"

// defaultEventLimit bounds one replay page read through Port.EventsAfter.
const defaultEventLimit = 128

var (
	// ErrNotSubscribed reports Poll on a card with no subscription.
	ErrNotSubscribed = errors.New("tasks: card is not subscribed")
	// ErrEmptyCardID reports Subscribe or Poll with a blank card id.
	ErrEmptyCardID = errors.New("tasks: empty card id")
)

// Card is the tracked state of a card read through Port. Status is stored as a
// plain string so the app layer does not import the domain status enum.
type Card struct {
	ID     string
	Status string
	Seq    uint64
}

// RecordedEvent is one event already stored in a card journal.
type RecordedEvent struct {
	Seq  uint64
	Type string
}

// Change is one status-change event produced by the monitor. Payload is filled
// by Port.Serialize, so the monitor never encodes bytes itself.
type Change struct {
	CardID  string
	Seq     uint64
	Type    string
	From    string
	To      string
	Payload []byte
}

// Port is the only seam through which the monitor touches the outside world.
// An adapter may read a file, a store or a network; the monitor itself holds no
// file, clock or network capability.
type Port interface {
	// Card returns the current state of id. ok is false when the card is absent.
	Card(ctx context.Context, id string) (card Card, ok bool, err error)
	// EventsAfter returns at most limit recorded events with seq greater than
	// cursor, in ascending seq order.
	EventsAfter(ctx context.Context, id string, cursor uint64, limit int) ([]RecordedEvent, error)
	// Serialize renders a produced change into its journal payload.
	Serialize(change Change) ([]byte, error)
}

// Result is one poll outcome. Changes are produced by the monitor; Events are
// journal entries read since the last poll. Both slices are non-nil, so an
// empty result never encodes as null.
type Result struct {
	Changes []Change
	Events  []RecordedEvent
}

type cardState struct {
	status string
	known  bool
	cursor uint64
}

// Monitor watches a set of cards. One Monitor serializes polls so two callers
// cannot emit the same change twice.
type Monitor struct {
	port  Port
	limit int

	mu      sync.Mutex
	states  map[string]*cardState
	nextSeq uint64
}

// NewMonitor builds a Monitor over port. A nil port is kept so Poll can return
// an error instead of panicking on bad wiring.
func NewMonitor(port Port) *Monitor {
	return &Monitor{
		port:   port,
		limit:  defaultEventLimit,
		states: make(map[string]*cardState),
	}
}

// NewMonitorWithLimit builds a Monitor with a custom replay page size. A
// non-positive limit selects the default.
func NewMonitorWithLimit(port Port, limit int) *Monitor {
	m := NewMonitor(port)
	if limit > 0 {
		m.limit = limit
	}
	return m
}

// Subscribe registers id for monitoring. Repeating a subscription for the same
// id changes nothing, so a caller can resubscribe after a restart without
// duplicating events or losing the last observed status.
func (m *Monitor) Subscribe(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrEmptyCardID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.states[id]; !ok {
		m.states[id] = &cardState{}
	}
	return nil
}

// Unsubscribe removes id. A later Subscribe starts a fresh baseline.
func (m *Monitor) Unsubscribe(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.states, strings.TrimSpace(id))
}

// Subscribed reports whether id is registered.
func (m *Monitor) Subscribed(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.states[strings.TrimSpace(id)]
	return ok
}

// SubscriptionCount reports how many distinct cards are registered.
func (m *Monitor) SubscriptionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.states)
}

// Poll reads one snapshot of a subscribed card. It emits exactly one change
// when the status differs from the last observed one, and returns journal
// events newer than the last cursor. A card seen for the first time sets the
// baseline without emitting a change. If any port call fails, Poll returns the
// error and leaves the stored state untouched, so a retry cannot lose an event.
func (m *Monitor) Poll(ctx context.Context, id string) (Result, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Result{}, ErrEmptyCardID
	}
	if m == nil || m.port == nil {
		return Result{}, errors.New("tasks: nil card monitor port")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.states[id]
	if !ok {
		return Result{}, fmt.Errorf("%w: %s", ErrNotSubscribed, id)
	}

	snapshot, found, err := m.port.Card(ctx, id)
	if err != nil {
		return Result{}, fmt.Errorf("tasks: read card %s: %w", id, err)
	}
	events, err := m.port.EventsAfter(ctx, id, state.cursor, m.limit)
	if err != nil {
		return Result{}, fmt.Errorf("tasks: read events for %s: %w", id, err)
	}
	if !found {
		// An absent card has no status to compare and no cursor to advance.
		return Result{}, nil
	}

	res := Result{Events: make([]RecordedEvent, 0, len(events))}
	cursor := state.cursor
	for _, ev := range events {
		if ev.Seq <= cursor {
			continue
		}
		res.Events = append(res.Events, ev)
		cursor = ev.Seq
	}

	var change Change
	emit := false
	if !state.known {
		state.known = true
		state.status = snapshot.Status
	} else if snapshot.Status != state.status {
		seq := m.nextSeq + 1
		change = Change{
			CardID: id,
			Seq:    seq,
			Type:   EventTypeStatusChanged,
			From:   state.status,
			To:     snapshot.Status,
		}
		payload, err := m.port.Serialize(change)
		if err != nil {
			return Result{}, fmt.Errorf("tasks: serialize change for %s: %w", id, err)
		}
		change.Payload = payload
		m.nextSeq = seq
		state.status = snapshot.Status
		emit = true
	}
	state.cursor = cursor
	if emit {
		res.Changes = []Change{change}
	}
	return res, nil
}

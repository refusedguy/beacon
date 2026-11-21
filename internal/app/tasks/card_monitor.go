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

// PollResult is one poll outcome. Changes are produced by the monitor; Events
// are journal entries read since the last poll. Both slices are non-nil, so an
// empty result never encodes as null. The name avoids the Worker Result type
// that lives in the same package.
type PollResult struct {
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

	pollMu  sync.Mutex
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
func (m *Monitor) Poll(ctx context.Context, id string) (PollResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return PollResult{}, ErrEmptyCardID
	}
	if m == nil || m.port == nil {
		return PollResult{}, errors.New("tasks: nil card monitor port")
	}

	// pollMu keeps the poll-to-poll guarantee. m.mu is released before every
	// port call, so a port callback can re-enter the monitor without deadlock.
	m.pollMu.Lock()
	defer m.pollMu.Unlock()

	// Snapshot the stored state under m.mu and work on the copy. No port call
	// runs while m.mu is held.
	m.mu.Lock()
	state, ok := m.states[id]
	if !ok {
		m.mu.Unlock()
		return PollResult{}, fmt.Errorf("%w: %s", ErrNotSubscribed, id)
	}
	st := *state
	m.mu.Unlock()

	snapshot, found, err := m.port.Card(ctx, id)
	if err != nil {
		return PollResult{}, fmt.Errorf("tasks: read card %s: %w", id, err)
	}
	events, err := m.port.EventsAfter(ctx, id, st.cursor, m.limit)
	if err != nil {
		return PollResult{}, fmt.Errorf("tasks: read events for %s: %w", id, err)
	}
	if !found {
		// An absent card has no status to compare and no cursor to advance.
		return PollResult{}, nil
	}

	res := PollResult{Events: make([]RecordedEvent, 0, len(events))}
	cursor := st.cursor
	for _, ev := range events {
		if ev.Seq <= cursor {
			continue
		}
		res.Events = append(res.Events, ev)
		cursor = ev.Seq
	}

	var change Change
	emit := false
	if !st.known {
		st.known = true
		st.status = snapshot.Status
	} else if snapshot.Status != st.status {
		m.mu.Lock()
		seq := m.nextSeq + 1
		m.mu.Unlock()
		change = Change{
			CardID: id,
			Seq:    seq,
			Type:   EventTypeStatusChanged,
			From:   st.status,
			To:     snapshot.Status,
		}
		payload, err := m.port.Serialize(change)
		if err != nil {
			return PollResult{}, fmt.Errorf("tasks: serialize change for %s: %w", id, err)
		}
		change.Payload = payload
		emit = true
	}
	st.cursor = cursor

	// Commit the copy under m.mu. Subscribe or Unsubscribe may have run while
	// the port calls were in flight, so check the entry again before writing.
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.states[id]
	if !ok {
		return PollResult{}, fmt.Errorf("%w: %s", ErrNotSubscribed, id)
	}
	if emit {
		m.nextSeq = change.Seq
		res.Changes = []Change{change}
	}
	*cur = st
	return res, nil
}

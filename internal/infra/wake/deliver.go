// Package wake implements the non-blocking wake acceptance of ADR 0082.
//
// A wake call is an admission, not an answer. Accept returns 202 with a
// deliveryId at once, starts the real work in the background and publishes
// events as hints. The final state comes only from a verdict on a separate
// channel: never from the 202 response and never from an agent status such as
// FINISHED, IDLE or agent_end. The adapter behaves the same in hosted and local
// mode (ADR 0082), and events are hints while state is the truth (ADR 0080).
package wake

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// DeliveryId ties one admission, its events and its verdict together (ADR
// 0081). It is opaque to the core.
type DeliveryId string

// Mode tells hosted and local execution apart. It changes where the handler
// runs, never the acknowledgement or the finality rule (ADR 0082).
type Mode string

const (
	Hosted Mode = "hosted"
	Local  Mode = "local"
)

// AcceptedStatus is the only status a wake admission returns.
const AcceptedStatus = 202

// Accepted is the whole 202 response: an acknowledgement and a deliveryId.
// It has no result, no outcome and no verdict, so finality can never be read
// from it.
type Accepted struct {
	Status     int        `json:"status"`
	DeliveryId DeliveryId `json:"deliveryId"`
}

// EventType names a published hint. A hint tells a listener what to re-read;
// it does not say that work has ended.
type EventType string

const (
	AcceptedEvent  EventType = "wake.accepted"
	DeliveredEvent EventType = "nudge.delivered"
)

// Event is a hint published on the event channel, always carrying the
// deliveryId of the admission it belongs to.
type Event struct {
	Type       EventType  `json:"type"`
	DeliveryId DeliveryId `json:"deliveryId"`
}

// Verdict is the only fact that can finish a task. Pending keeps it open;
// Ready is final.
type Verdict string

const (
	Pending Verdict = "pending"
	Ready   Verdict = "ready"
)

// AgentSignal is a terminal-looking status from an agent. None of them finish
// a task: FINISHED and IDLE come from Cursor, agent_end from Pi, and ADR 0082
// forbids reading finality from any of them.
type AgentSignal string

const (
	Finished AgentSignal = "FINISHED"
	Idle     AgentSignal = "IDLE"
	AgentEnd AgentSignal = "agent_end"
)

// Request is one wake admission. An empty DeliveryId is generated. Mode is
// recorded for observability but never changes the acknowledgement.
type Request struct {
	Target     string
	Payload    string
	DeliveryId DeliveryId
	Mode       Mode
}

// Work is the background job started after an admission. It receives the same
// deliveryId as the 202 response.
type Work func(ctx context.Context, req Request, id DeliveryId)

// Deliverer admits wake calls without waiting for results. It owns the event
// and verdict channels and the per-delivery state, and is safe for concurrent
// callers.
type Deliverer struct {
	work     Work
	events   chan Event
	verdicts chan Verdict
	seen     map[DeliveryId]struct{}
	states   map[DeliveryId]Verdict
	nextID   atomic.Uint64
	mu       sync.Mutex
}

// New builds a Deliverer that runs work in the background after each
// admission. A nil work is allowed and leaves the delivery as admitted only.
func New(work Work) *Deliverer {
	return &Deliverer{
		work:     work,
		events:   make(chan Event, 64),
		verdicts: make(chan Verdict, 16),
		seen:     make(map[DeliveryId]struct{}),
		states:   make(map[DeliveryId]Verdict),
	}
}

// Accept admits one wake call. It returns 202 with a deliveryId immediately,
// before any background work runs. A repeated deliveryId is a duplicate: it
// returns the same acknowledgement and starts no second job (ADR 0081).
func (d *Deliverer) Accept(ctx context.Context, req Request) Accepted {
	id := req.DeliveryId
	if id == "" {
		id = DeliveryId(fmt.Sprintf("dlv_%d", d.nextID.Add(1)))
	}

	d.mu.Lock()
	if _, duplicate := d.seen[id]; duplicate {
		d.mu.Unlock()
		return Accepted{Status: AcceptedStatus, DeliveryId: id}
	}
	d.seen[id] = struct{}{}
	d.states[id] = Pending
	d.mu.Unlock()

	d.Publish(Event{Type: AcceptedEvent, DeliveryId: id})
	go func() {
		d.Publish(Event{Type: DeliveredEvent, DeliveryId: id})
		if d.work != nil {
			d.work(ctx, req, id)
		}
	}()

	return Accepted{Status: AcceptedStatus, DeliveryId: id}
}

// Publish emits a hint. It never blocks the caller: a full buffer drops the
// hint instead of delaying an admission or a decision.
func (d *Deliverer) Publish(e Event) {
	select {
	case d.events <- e:
	default:
	}
}

// Events is the hint channel. A reader must never treat an event as final.
func (d *Deliverer) Events() <-chan Event { return d.events }

// Verdicts is the decision channel. Only Ready on this channel finishes work.
func (d *Deliverer) Verdicts() <-chan Verdict { return d.verdicts }

// Decide records a decision from the verdict channel. Only Ready flips the
// state and publishes on the verdict channel; anything else leaves the task
// open. An unknown deliveryId is ignored.
func (d *Deliverer) Decide(id DeliveryId, v Verdict) {
	d.mu.Lock()
	if _, known := d.states[id]; !known {
		d.mu.Unlock()
		return
	}
	if v == Ready {
		d.states[id] = Ready
	}
	d.mu.Unlock()
	if v == Ready {
		select {
		case d.verdicts <- Ready:
		default:
		}
	}
}

// Observe records an agent status without treating it as final. FINISHED, IDLE
// and agent_end are hints at most; the state stays Pending until a verdict.
func (d *Deliverer) Observe(id DeliveryId, signal AgentSignal) Verdict {
	_ = signal // ADR 0082: an agent status is never a verdict.
	return d.State(id)
}

// State is the source of truth for one admission. It is Pending until an
// explicit Ready verdict arrives.
func (d *Deliverer) State(id DeliveryId) Verdict {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.states[id]
}

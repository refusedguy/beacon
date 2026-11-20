// Package notify routes events to delivery targets through injected ports.
// The router owns no transport, store or clock. A Resolver decides which
// targets match an event, and a Deliverer sends one notification to one
// target. Concrete adapters are composed by the caller, so this package stays
// free of store, wake and transport imports.
package notify

import (
	"context"
	"errors"
	"sync"
)

// Key identifies a route. EventType names the kind of event and Recipient
// names the logical destination, for example a ses_... session id.
type Key struct {
	EventType string
	Recipient string
}

// Event is the input to the router. Payload is opaque to the router and is
// passed through to the Deliverer unchanged.
type Event struct {
	Type      string
	Recipient string
	Payload   []byte
}

// Key returns the route key for the event.
func (e Event) Key() Key {
	return Key{EventType: e.Type, Recipient: e.Recipient}
}

// Target is one concrete delivery endpoint. It is opaque to the router; the
// Deliverer interprets it.
type Target string

// Notification is the message handed to a Deliverer for one target.
type Notification struct {
	Key     Key
	Target  Target
	Payload []byte
}

// Resolver maps a route key to the targets that should receive the event. A
// nil or empty slice means no route matched. An error reports a resolver
// failure; the router stops and returns it without delivering anything.
type Resolver interface {
	Resolve(ctx context.Context, key Key) ([]Target, error)
}

// ResolverFunc adapts a function to Resolver.
type ResolverFunc func(ctx context.Context, key Key) ([]Target, error)

// Resolve calls f with key.
func (f ResolverFunc) Resolve(ctx context.Context, key Key) ([]Target, error) {
	return f(ctx, key)
}

// Deliverer sends one notification to one target. It is the port that owns
// any transport or I/O. A returned error is recorded in the report and does
// not stop delivery to the remaining targets.
type Deliverer interface {
	Deliver(ctx context.Context, n Notification) error
}

// DelivererFunc adapts a function to Deliverer.
type DelivererFunc func(ctx context.Context, n Notification) error

// Deliver calls f with n.
func (f DelivererFunc) Deliver(ctx context.Context, n Notification) error {
	return f(ctx, n)
}

// Failure records one target the Deliverer could not reach.
type Failure struct {
	Target Target
	Err    error
}

// Report is the outcome of one Route call.
type Report struct {
	Delivered int
	NoRoute   bool
	Failures  []Failure
}

var (
	// ErrNilResolver reports a Router with no resolver port.
	ErrNilResolver = errors.New("notify: nil resolver")
	// ErrNilDeliverer reports a Router with no deliverer port.
	ErrNilDeliverer = errors.New("notify: nil deliverer")
)

// Router resolves an event to targets and delivers to each one through the
// injected ports. The router keeps no mutable state, so it is safe to share
// across goroutines as long as the ports are.
type Router struct {
	resolver  Resolver
	deliverer Deliverer
}

// New builds a Router over resolver and deliverer.
func New(resolver Resolver, deliverer Deliverer) *Router {
	return &Router{resolver: resolver, deliverer: deliverer}
}

// Route resolves event and delivers one Notification per target. A resolver
// error is returned and nothing is delivered. Delivery errors go into the
// report; the router still tries the remaining targets.
func (r *Router) Route(ctx context.Context, event Event) (Report, error) {
	if r == nil || r.resolver == nil {
		return Report{}, ErrNilResolver
	}
	if r.deliverer == nil {
		return Report{}, ErrNilDeliverer
	}
	if ctx == nil {
		ctx = context.Background()
	}

	key := event.Key()
	targets, err := r.resolver.Resolve(ctx, key)
	if err != nil {
		return Report{}, err
	}

	var report Report
	if len(targets) == 0 {
		report.NoRoute = true
		return report, nil
	}
	for _, target := range targets {
		n := Notification{Key: key, Target: target, Payload: event.Payload}
		if err := r.deliverer.Deliver(ctx, n); err != nil {
			report.Failures = append(report.Failures, Failure{Target: target, Err: err})
			continue
		}
		report.Delivered++
	}
	return report, nil
}

// RouteTable is an in-memory Resolver. It holds route rules in a map and does
// no I/O. Callers can use it directly or replace it with a store-backed
// Resolver that implements the same port.
type RouteTable struct {
	mu    sync.RWMutex
	rules map[Key][]Target
}

// NewRouteTable builds an empty RouteTable.
func NewRouteTable() *RouteTable {
	return &RouteTable{rules: make(map[Key][]Target)}
}

// Add registers targets for key. Earlier targets registered under the key are
// kept, and the new targets are appended in the given order.
func (t *RouteTable) Add(key Key, targets ...Target) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rules[key] = append(t.rules[key], targets...)
}

// Resolve returns a copy of the targets registered for key. A key with no
// targets yields nil and no error.
func (t *RouteTable) Resolve(_ context.Context, key Key) ([]Target, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	targets := t.rules[key]
	if len(targets) == 0 {
		return nil, nil
	}
	out := make([]Target, len(targets))
	copy(out, targets)
	return out, nil
}

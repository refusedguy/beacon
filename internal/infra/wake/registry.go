// Package wake selects a wake deliverer by the subscriber's adapter name.
// The registry keeps names as opaque strings: the core never learns which
// harness a name stands for, and a new adapter is added with one Register call.
package wake

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/refusedguy/beacon/internal/domain"
)

// Supported adapter names. They are opaque identifiers, not harness code paths.
const (
	AdapterOpenCode = "opencode"
	AdapterACP      = "acp"
	AdapterPi       = "pi"
	AdapterKilo     = "kilo"
	AdapterCursor   = "cursor"
)

// defaultAdapter is used when the subscriber names no adapter or names one the
// registry does not know. A wake must never be dropped because of a bad name.
const defaultAdapter = AdapterOpenCode

// ErrUnsupported marks an honestly unimplemented adapter. It is an error, never
// a successful delivery: a caller must not read it as an accepted wake.
var ErrUnsupported = errors.New("wake: unsupported")

// unsupportedDeliverer is the stub registered for adapters with no delivery
// implementation. It reports the refusal instead of faking a send or an ack.
type unsupportedDeliverer struct {
	adapter string
}

var _ domain.WakeDeliverer = unsupportedDeliverer{}

func (u unsupportedDeliverer) Deliver(context.Context, string, domain.WakePayload) error {
	return fmt.Errorf("%w adapter %q: no delivery implementation registered", ErrUnsupported, u.adapter)
}

// builtinDeliverer returns the built-in deliverer for a supported name, or nil
// for an unknown name. It is the single source of truth for the five built-ins;
// Registry.Register can replace any of them with a configured implementation.
func builtinDeliverer(adapter string) domain.WakeDeliverer {
	switch adapter {
	case AdapterOpenCode:
		return &OpenCodeDeliverer{}
	case AdapterACP:
		return &ACPAdapter{}
	case AdapterPi, AdapterKilo, AdapterCursor:
		return unsupportedDeliverer{adapter: adapter}
	default:
		return nil
	}
}

// Registry satisfies the adapter-aware wake port, so a verdict source such as
// cibridge can pass each subscriber's adapter straight into Deliver.
var _ domain.AdapterWakeDeliverer = (*Registry)(nil)

// Registry maps an adapter name to a domain.WakeDeliverer. The zero value is
// usable: Deliver falls back to the built-in deliverers when nothing is
// registered. Register is safe to call concurrently with Deliver.
type Registry struct {
	mu         sync.RWMutex
	deliverers map[string]domain.WakeDeliverer
}

// NewRegistry returns a registry with all five supported adapters registered.
// The opencode adapter is the fallback for an empty or unknown name.
func NewRegistry() *Registry {
	r := &Registry{}
	for _, name := range []string{AdapterOpenCode, AdapterACP, AdapterPi, AdapterKilo, AdapterCursor} {
		r.Register(name, builtinDeliverer(name))
	}
	return r
}

// Register adds or replaces the deliverer for adapter. An empty name or a nil
// deliverer is ignored so a bad registration cannot disable the fallback.
func (r *Registry) Register(adapter string, d domain.WakeDeliverer) {
	if adapter == "" || d == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deliverers == nil {
		r.deliverers = make(map[string]domain.WakeDeliverer)
	}
	r.deliverers[adapter] = d
}

// Deliver resolves adapter to a deliverer and hands it the wake pointer. An
// empty or unknown name falls back to opencode; a registered stub returns an
// ErrUnsupported error and never reports a false success.
func (r *Registry) Deliver(ctx context.Context, adapter, sessionID string, p domain.WakePayload) error {
	name := adapter
	if name == "" {
		name = defaultAdapter
	}

	r.mu.RLock()
	d := r.deliverers[name]
	fallback := r.deliverers[defaultAdapter]
	r.mu.RUnlock()

	if d == nil {
		d = builtinDeliverer(name)
	}
	if d == nil {
		d = fallback
	}
	if d == nil {
		d = builtinDeliverer(defaultAdapter)
	}
	if d == nil {
		return fmt.Errorf("wake: registry: no deliverer for adapter %q or fallback %q", adapter, defaultAdapter)
	}
	return d.Deliver(ctx, sessionID, p)
}

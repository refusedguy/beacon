// Package wake selects a wake deliverer by the subscriber's adapter name.
// The registry keeps names as opaque strings: the core never learns which
// harness a name stands for, and a new adapter is added with one Register call.
package wake

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// DeliverToSubscriber dispatches a wake to one subscriber of w. The address
// and the obligation are both checked against that subscriber before an
// adapter is chosen, so no other party's session can receive the pointer.
func (r *Registry) DeliverToSubscriber(ctx context.Context, w domain.Watch, p domain.WakePayload, subscriberID string) error {
	target, err := SubscriberTarget(w, p.WatchID, subscriberID)
	if err != nil {
		return err
	}
	if want := domain.ObligationKey(p.WatchID, p.Revision, target); p.ObligationID != want {
		return fmt.Errorf("wake: obligation %q does not belong to subscriber %q of watch %s", p.ObligationID, target, p.WatchID)
	}
	adapter := ""
	for _, sub := range w.Subscribers {
		if sub.ID == target {
			adapter = sub.Adapter
			break
		}
	}
	return r.Deliver(ctx, adapter, target, p)
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

// ===== Pi RPC provider (T3.217) =====
// The provider owns no process. A caller injects one long-lived connection,
// so the same probe runs against a real Pi socket, a test transport or any
// other implementation of PiRPCConnection.

// PiRPCCall is one method invocation on a Pi connection. The transport decides
// how these fields map onto its wire format.
type PiRPCCall struct {
	SessionID string `json:"sessionId"`
	Text      string `json:"text,omitempty"`
}

// PiRPCReply is the provider-neutral result of one call. A transport decodes
// wire frames into it; the probe never reads a process stream.
type PiRPCReply struct {
	State     string `json:"state"`
	MessageID string `json:"messageId"`
	Error     string `json:"error"`
}

// PiRPCConnection is the injected transport dependency. Its owner controls
// process and socket lifetime. Call returns one reply for one method, and
// Close is called by the owner when the connection is no longer needed.
type PiRPCConnection interface {
	Call(ctx context.Context, method string, call PiRPCCall) (PiRPCReply, error)
	Close() error
}

// PiProbe is the provider interface. Prompt writes into the active turn,
// Steer writes into a running session and FollowUp queues into a sleeping one.
// GetState and WaitForIdle expose the state transition W05 checks.
type PiProbe interface {
	Prompt(ctx context.Context, sessionID, text string) error
	Steer(ctx context.Context, sessionID, text string) error
	FollowUp(ctx context.Context, sessionID, text string) error
	Abort(ctx context.Context, sessionID string) error
	GetState(ctx context.Context, sessionID string) (string, error)
	WaitForIdle(ctx context.Context, sessionID string) error
}

// PiRPCProbe implements PiProbe over an injected PiRPCConnection. It holds
// transport as a dependency and never starts a process or opens a socket.
type PiRPCProbe struct {
	Conn PiRPCConnection
}

var _ PiProbe = (*PiRPCProbe)(nil)

func (p *PiRPCProbe) call(ctx context.Context, method, sessionID, text string) (PiRPCReply, error) {
	if p == nil || p.Conn == nil {
		return PiRPCReply{}, fmt.Errorf("%w: pi rpc %s: no connection", ErrUnsupported, method)
	}
	reply, err := p.Conn.Call(ctx, method, PiRPCCall{SessionID: sessionID, Text: text})
	if err != nil {
		return PiRPCReply{}, fmt.Errorf("wake: pi rpc %s: %w", method, err)
	}
	if reply.Error != "" {
		return PiRPCReply{}, fmt.Errorf("wake: pi rpc %s: %s", method, reply.Error)
	}
	return reply, nil
}

func (p *PiRPCProbe) Prompt(ctx context.Context, sessionID, text string) error {
	_, err := p.call(ctx, "prompt", sessionID, text)
	return err
}

func (p *PiRPCProbe) Steer(ctx context.Context, sessionID, text string) error {
	_, err := p.call(ctx, "steer", sessionID, text)
	return err
}

func (p *PiRPCProbe) FollowUp(ctx context.Context, sessionID, text string) error {
	_, err := p.call(ctx, "followUp", sessionID, text)
	return err
}

func (p *PiRPCProbe) Abort(ctx context.Context, sessionID string) error {
	_, err := p.call(ctx, "abort", sessionID, "")
	return err
}

func (p *PiRPCProbe) GetState(ctx context.Context, sessionID string) (string, error) {
	reply, err := p.call(ctx, "get_state", sessionID, "")
	if err != nil {
		return "", err
	}
	return reply.State, nil
}

func (p *PiRPCProbe) WaitForIdle(ctx context.Context, sessionID string) error {
	_, err := p.call(ctx, "waitForIdle", sessionID, "")
	return err
}

// PiDeliveryMode is the ADR 0026 outcome a Pi delivery asks for. Steer writes
// into the running turn; queue leaves a followUp for a sleeping session.
type PiDeliveryMode string

const (
	PiModeQueue PiDeliveryMode = "queue"
	PiModeSteer PiDeliveryMode = "steer"
)

// ParsePiDeliveryMode treats an empty value as the default queue mode.
func ParsePiDeliveryMode(raw string) (PiDeliveryMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(PiModeQueue):
		return PiModeQueue, nil
	case string(PiModeSteer):
		return PiModeSteer, nil
	default:
		return "", fmt.Errorf("wake: pi mode %q: want queue or steer", raw)
	}
}

// PiDeliverer adapts a PiProbe to the wake deliverer port. Mode selects the
// RPC method; the default queue mode uses followUp and is reported as such.
type PiDeliverer struct {
	Probe PiProbe
	Mode  PiDeliveryMode
}

var _ domain.WakeDeliverer = (*PiDeliverer)(nil)

func (d *PiDeliverer) Deliver(ctx context.Context, sessionID string, p domain.WakePayload) error {
	text := fmt.Sprintf("beacon wake watch=%s revision=%d verdict=%s obligation=%s",
		p.WatchID, p.Revision, p.Verdict, p.ObligationID)
	_, err := d.DeliverText(ctx, sessionID, text)
	return err
}

// DeliverText writes one pointer and returns the RPC method used. A nil error
// means the provider accepted the write; callers report queue versus steer
// from the mode instead of turning a successful followUp into a failure.
func (d *PiDeliverer) DeliverText(ctx context.Context, sessionID, text string) (PiDeliveryMode, error) {
	if d == nil || d.Probe == nil {
		return PiModeQueue, fmt.Errorf("%w adapter %q: no pi probe", ErrUnsupported, AdapterPi)
	}
	mode := d.Mode
	if mode == "" {
		mode = PiModeQueue
	}
	switch mode {
	case PiModeQueue:
		return PiModeQueue, d.Probe.FollowUp(ctx, sessionID, text)
	case PiModeSteer:
		return PiModeSteer, d.Probe.Steer(ctx, sessionID, text)
	default:
		return PiModeQueue, fmt.Errorf("%w: pi mode %q", ErrUnsupported, mode)
	}
}

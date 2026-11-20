package wake

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

// registryStub records the delivery it was handed so a test can tell which
// adapter the registry chose.
type registryStub struct {
	calls    int
	sessions []string
	payloads []domain.WakePayload
	err      error
}

var _ domain.WakeDeliverer = (*registryStub)(nil)

func (s *registryStub) Deliver(_ context.Context, sessionID string, p domain.WakePayload) error {
	s.calls++
	s.sessions = append(s.sessions, sessionID)
	s.payloads = append(s.payloads, p)
	return s.err
}

func testWakePayload() domain.WakePayload {
	return domain.WakePayload{
		WatchID:      "watch_1",
		ObligationID: "obl_1",
		Revision:     2,
		Verdict:      "retry",
	}
}

func TestRegistrySelectsRegisteredAdapter(t *testing.T) {
	r := NewRegistry()
	open := &registryStub{}
	acp := &registryStub{}
	r.Register(AdapterOpenCode, open)
	r.Register(AdapterACP, acp)

	p := testWakePayload()
	if err := r.Deliver(context.Background(), AdapterACP, "sess_acp", p); err != nil {
		t.Fatalf("Deliver(acp): %v", err)
	}
	if acp.calls != 1 || open.calls != 0 {
		t.Fatalf("calls: acp=%d opencode=%d, want 1 and 0", acp.calls, open.calls)
	}
	if acp.sessions[0] != "sess_acp" || acp.payloads[0] != p {
		t.Fatalf("acp got session %q payload %+v", acp.sessions[0], acp.payloads[0])
	}

	if err := r.Deliver(context.Background(), AdapterOpenCode, "sess_open", p); err != nil {
		t.Fatalf("Deliver(opencode): %v", err)
	}
	if open.calls != 1 || acp.calls != 1 {
		t.Fatalf("calls: opencode=%d acp=%d, want 1 and 1", open.calls, acp.calls)
	}
	if open.sessions[0] != "sess_open" || open.payloads[0] != p {
		t.Fatalf("opencode got session %q payload %+v", open.sessions[0], open.payloads[0])
	}
}

func TestRegistryFallsBackToOpenCode(t *testing.T) {
	for _, adapter := range []string{"", "ghost-adapter"} {
		t.Run(adapter, func(t *testing.T) {
			r := NewRegistry()
			open := &registryStub{}
			other := &registryStub{}
			r.Register(AdapterOpenCode, open)
			r.Register(AdapterACP, other)

			if err := r.Deliver(context.Background(), adapter, "sess_fallback", testWakePayload()); err != nil {
				t.Fatalf("Deliver(%q): %v", adapter, err)
			}
			if open.calls != 1 {
				t.Fatalf("opencode calls = %d, want 1", open.calls)
			}
			if other.calls != 0 {
				t.Fatalf("acp calls = %d, want 0", other.calls)
			}
			if open.sessions[0] != "sess_fallback" {
				t.Fatalf("session = %q, want sess_fallback", open.sessions[0])
			}
		})
	}
}

func TestRegistryUnsupportedAdaptersReportErrUnsupported(t *testing.T) {
	for _, adapter := range []string{AdapterPi, AdapterKilo, AdapterCursor} {
		t.Run(adapter, func(t *testing.T) {
			r := NewRegistry()
			open := &registryStub{}
			r.Register(AdapterOpenCode, open)

			err := r.Deliver(context.Background(), adapter, "sess_stub", testWakePayload())
			if err == nil {
				t.Fatal("Deliver on an unsupported adapter returned nil, a false ok")
			}
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("err = %v, want ErrUnsupported", err)
			}
			if !strings.Contains(err.Error(), adapter) {
				t.Fatalf("err = %v, want the adapter name %q", err, adapter)
			}
			if open.calls != 0 {
				t.Fatalf("fallback delivered %d wake(s) for unsupported adapter", open.calls)
			}
		})
	}
}

func TestRegistryUnsupportedIsNotMaskedByFallback(t *testing.T) {
	// A stub must not be replaced by the opencode fallback just because the
	// name is known but unconfigured: the refusal is the honest outcome.
	r := NewRegistry()
	open := &registryStub{}
	r.Register(AdapterOpenCode, open)

	for _, adapter := range []string{AdapterPi, AdapterKilo, AdapterCursor} {
		if err := r.Deliver(context.Background(), adapter, "sess", domain.WakePayload{}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Deliver(%q) = %v, want ErrUnsupported", adapter, err)
		}
	}
	if open.calls != 0 {
		t.Fatalf("fallback calls = %d, want 0: unsupported must not be delivered through opencode", open.calls)
	}
}

func TestRegisterCanReplaceUnsupportedAdapter(t *testing.T) {
	r := NewRegistry()
	pi := &registryStub{}
	r.Register(AdapterPi, pi)

	p := testWakePayload()
	if err := r.Deliver(context.Background(), AdapterPi, "sess_pi", p); err != nil {
		t.Fatalf("Deliver(pi) after Register: %v", err)
	}
	if pi.calls != 1 || pi.sessions[0] != "sess_pi" || pi.payloads[0] != p {
		t.Fatalf("pi stub got %+v, want one delivery of %q/%+v", pi, "sess_pi", p)
	}
}

func TestRegisterCanAddNewAdapter(t *testing.T) {
	r := NewRegistry()
	custom := &registryStub{}
	r.Register("future-harness", custom)

	if err := r.Deliver(context.Background(), "future-harness", "sess_new", testWakePayload()); err != nil {
		t.Fatalf("Deliver(future-harness): %v", err)
	}
	if custom.calls != 1 {
		t.Fatalf("custom calls = %d, want 1", custom.calls)
	}
}

func TestBuiltinDeliverersUseExistingAdapters(t *testing.T) {
	if _, ok := builtinDeliverer(AdapterACP).(*ACPAdapter); !ok {
		t.Fatalf("builtin acp = %T, want *ACPAdapter", builtinDeliverer(AdapterACP))
	}
	if _, ok := builtinDeliverer(AdapterOpenCode).(*OpenCodeDeliverer); !ok {
		t.Fatalf("builtin opencode = %T, want *OpenCodeDeliverer", builtinDeliverer(AdapterOpenCode))
	}
	if got := builtinDeliverer("unknown"); got != nil {
		t.Fatalf("builtin unknown = %T, want nil", got)
	}
}

func TestZeroValueRegistryFallsBackToOpenCode(t *testing.T) {
	var r Registry
	// The zero value has no registered stub for pi, so an unknown/unconfigured
	// name resolves to the built-in opencode adapter. A nil delivery would be
	// indistinguishable from a dropped wake; ensure we get a real deliverer via
	// a registered opencode stub instead.
	open := &registryStub{}
	r.Register(AdapterOpenCode, open)

	if err := r.Deliver(context.Background(), "", "sess_zero", testWakePayload()); err != nil {
		t.Fatalf("Deliver with zero registry: %v", err)
	}
	if open.calls != 1 {
		t.Fatalf("opencode calls = %d, want 1", open.calls)
	}
}

func TestRegisterIgnoresInvalidInput(t *testing.T) {
	r := NewRegistry()
	pi := &registryStub{}
	r.Register(AdapterPi, pi)
	r.Register("", &registryStub{})
	r.Register(AdapterPi, nil)

	if err := r.Deliver(context.Background(), AdapterPi, "sess", domain.WakePayload{}); err != nil {
		t.Fatalf("Deliver(pi): %v, want the previously registered stub to stay", err)
	}
	if pi.calls != 1 {
		t.Fatalf("pi calls = %d, want 1", pi.calls)
	}
}

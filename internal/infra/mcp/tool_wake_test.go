package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeWakeSender is an in-memory WakeSender. It records the call and returns a
// fixed delivery and reason so tests can exercise each honest outcome without
// a real environment adapter.
type fakeWakeSender struct {
	delivery   Delivery
	reason     string
	calls      int
	lastTarget string
	lastText   string
}

func (f *fakeWakeSender) Deliver(_ context.Context, target, text string) (Delivery, string) {
	f.calls++
	f.lastTarget = target
	f.lastText = text
	return f.delivery, f.reason
}

// connectWakeServer drives an already-built server through in-memory
// transports. It lets a test attach the wake tool first, then inspect tools or
// call them over the same MCP path CI exercises.
func connectWakeServer(t *testing.T, server *mcpsdk.Server) *mcpsdk.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "beacon-wake-test", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})
	return clientSession
}

// TestWakeDeliveryModes proves that both real delivery modes are surfaced as
// success. These are the two branches ADR 0017's ladder can act on.
func TestWakeDeliveryModes(t *testing.T) {
	cases := []struct {
		name     string
		delivery Delivery
	}{
		{name: "steer", delivery: DeliverySteer},
		{name: "queue", delivery: DeliveryQueue},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender := &fakeWakeSender{delivery: tc.delivery}
			handler := NewWakeHandler(sender)

			out, err := handler.Wake(context.Background(), WakeInput{Target: "session-1", Text: "obligation-1@7"})
			if err != nil {
				t.Fatalf("Wake() error = %v, want nil", err)
			}
			if out.Delivery != tc.delivery {
				t.Errorf("delivery = %q, want %q", out.Delivery, tc.delivery)
			}
			if !out.OK {
				t.Errorf("ok = false for %q, want true", tc.delivery)
			}
			if out.Reason != "" {
				t.Errorf("reason = %q, want empty on success", out.Reason)
			}
			if sender.calls != 1 {
				t.Errorf("sender calls = %d, want 1", sender.calls)
			}
			if sender.lastTarget != "session-1" || sender.lastText != "obligation-1@7" {
				t.Errorf("sender got target=%q text=%q, want session-1 and obligation-1@7", sender.lastTarget, sender.lastText)
			}
		})
	}
}

// TestWakeUnsupportedIsNotSuccess is the acceptance guard: an environment that
// cannot deliver must answer honestly and must not be counted as success.
func TestWakeUnsupportedIsNotSuccess(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliveryUnsupported, reason: "no open session channel"}
	handler := NewWakeHandler(sender)

	out, err := handler.Wake(context.Background(), WakeInput{Target: "session-1", Text: "obligation-1@7"})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil: unsupported is an honest answer, not a transport error", err)
	}
	if out.Delivery != DeliveryUnsupported {
		t.Errorf("delivery = %q, want %q", out.Delivery, DeliveryUnsupported)
	}
	if out.OK {
		t.Error("ok = true for unsupported, want false")
	}
	if out.Reason != "no open session channel" {
		t.Errorf("reason = %q, want the adapter's refusal reason", out.Reason)
	}
}

// TestWakeUnsupportedReasonIsMandatory keeps a refusal explainable even when
// the adapter forgets to supply a reason.
func TestWakeUnsupportedReasonIsMandatory(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliveryUnsupported}
	out, err := NewWakeHandler(sender).Wake(context.Background(), WakeInput{Target: "s", Text: "p"})
	if err != nil {
		t.Fatalf("Wake() error = %v", err)
	}
	if out.OK {
		t.Error("ok = true for unsupported without reason, want false")
	}
	if strings.TrimSpace(out.Reason) == "" {
		t.Error("reason is empty for unsupported, want a default explanation")
	}
}

// TestWakeUnknownDeliveryIsUnsupported blocks a sender from inventing a new
// delivery value and having it read as success.
func TestWakeUnknownDeliveryIsUnsupported(t *testing.T) {
	sender := &fakeWakeSender{delivery: Delivery("bogus")}
	out, err := NewWakeHandler(sender).Wake(context.Background(), WakeInput{Target: "s", Text: "p"})
	if err != nil {
		t.Fatalf("Wake() error = %v", err)
	}
	if out.Delivery != DeliveryUnsupported {
		t.Errorf("delivery = %q, want %q for an unknown value", out.Delivery, DeliveryUnsupported)
	}
	if out.OK {
		t.Error("ok = true for an unknown delivery value, want false")
	}
	if !strings.Contains(out.Reason, "bogus") {
		t.Errorf("reason = %q, want it to name the unknown value", out.Reason)
	}
}

// TestWakeNilSenderIsUnsupported proves a missing adapter reports a refusal
// instead of fabricating a delivered follow-up.
func TestWakeNilSenderIsUnsupported(t *testing.T) {
	out, err := NewWakeHandler(nil).Wake(context.Background(), WakeInput{Target: "s", Text: "p"})
	if err != nil {
		t.Fatalf("Wake() error = %v", err)
	}
	if out.Delivery != DeliveryUnsupported || out.OK {
		t.Errorf("got delivery=%q ok=%v, want unsupported/false", out.Delivery, out.OK)
	}
	if out.Reason == "" {
		t.Error("reason is empty for a nil sender, want an explanation")
	}
}

// TestWakeRejectsMissingInput validates the MCP boundary. No delivery is
// attempted when the pointer or the target is absent.
func TestWakeRejectsMissingInput(t *testing.T) {
	cases := map[string]WakeInput{
		"empty target": {Text: "p"},
		"empty text":   {Target: "s"},
		"both empty":   {},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			sender := &fakeWakeSender{delivery: DeliverySteer}
			if _, err := NewWakeHandler(sender).Wake(context.Background(), in); err == nil {
				t.Fatal("Wake() error = nil, want an invalid-input error")
			}
			if sender.calls != 0 {
				t.Errorf("sender calls = %d, want 0 for invalid input", sender.calls)
			}
		})
	}
}

// TestAttachWakeRegistersOneTool checks the integration hook without wiring
// the tool into NewServer. A repeated attach must not duplicate the tool.
func TestAttachWakeRegistersOneTool(t *testing.T) {
	server := NewServer()
	AttachWake(server, NewWakeHandler(&fakeWakeSender{delivery: DeliveryQueue}))
	AttachWake(server, NewWakeHandler(&fakeWakeSender{delivery: DeliveryQueue}))

	session := connectWakeServer(t, server)
	count := 0
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		if tool.Name == WakeToolName {
			count++
		}
	}
	if count != 1 {
		t.Errorf("beacon_wake registered %d times, want 1 (AttachWake must be idempotent)", count)
	}
}

// TestNewServerDoesNotRegisterWake guards the separation of concerns: the wake
// tool is a later integration and must not appear in the default server.
func TestNewServerDoesNotRegisterWake(t *testing.T) {
	session := connectWakeServer(t, NewServer())
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		if tool.Name == WakeToolName {
			t.Fatalf("NewServer registered %q, want the stub set only", WakeToolName)
		}
	}
}

// TestWakeOverInMemoryTransport drives the tool through a real MCP client and
// server session. The delivery field is proven to reach the client, not just
// the in-process handler.
func TestWakeOverInMemoryTransport(t *testing.T) {
	server := NewServer()
	AttachWake(server, NewWakeHandler(&fakeWakeSender{delivery: DeliveryQueue}))
	session := connectWakeServer(t, server)

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: WakeToolName,
		Arguments: WakeInput{
			Target: "session-1",
			Text:   "obligation-1@7",
		},
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", WakeToolName, err)
	}

	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out WakeOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
	if out.Delivery != DeliveryQueue || !out.OK {
		t.Errorf("client saw delivery=%q ok=%v, want queue/true", out.Delivery, out.OK)
	}
}

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
	lastSource WakeSource
}

func (f *fakeWakeSender) Deliver(_ context.Context, target, text string, source WakeSource) (Delivery, string) {
	f.calls++
	f.lastTarget = target
	f.lastText = text
	f.lastSource = source
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

// TestWakeInterruptsWhenHumanNeeded is the T2.24 acceptance test (ADR 0028).
// Either human flag stops the wait before any delivery is attempted and the
// response names the cause. Both flags together name both causes.
func TestWakeInterruptsWhenHumanNeeded(t *testing.T) {
	cases := []struct {
		name       string
		in         WakeInput
		reasonPart string
		alsoPart   string
	}{
		{
			name: "pending question",
			in: WakeInput{
				Target:             "session-1",
				Text:               "obligation-1@7",
				HasPendingQuestion: true,
			},
			reasonPart: "question",
		},
		{
			name: "pending approval",
			in: WakeInput{
				Target:          "session-1",
				Text:            "obligation-1@7",
				PendingApproval: true,
			},
			reasonPart: "approval",
		},
		{
			name: "both flags",
			in: WakeInput{
				Target:             "session-1",
				Text:               "obligation-1@7",
				HasPendingQuestion: true,
				PendingApproval:    true,
			},
			reasonPart: "question",
			alsoPart:   "approval",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender := &fakeWakeSender{delivery: DeliverySteer}
			out, err := NewWakeHandler(sender).Wake(context.Background(), tc.in)
			if err != nil {
				t.Fatalf("Wake() error = %v, want nil", err)
			}
			if !out.Interrupted {
				t.Error("interrupted = false, want true")
			}
			if out.Delivery != DeliveryInterrupted {
				t.Errorf("delivery = %q, want %q", out.Delivery, DeliveryInterrupted)
			}
			if out.OK {
				t.Error("ok = true on interruption, want false (no follow-up delivered)")
			}
			if strings.TrimSpace(out.Reason) == "" {
				t.Error("reason is empty on interruption, want an explanation")
			}
			if !strings.Contains(out.Reason, tc.reasonPart) {
				t.Errorf("reason = %q, want it to mention %q", out.Reason, tc.reasonPart)
			}
			if tc.alsoPart != "" && !strings.Contains(out.Reason, tc.alsoPart) {
				t.Errorf("reason = %q, want it to mention %q", out.Reason, tc.alsoPart)
			}
			if sender.calls != 0 {
				t.Errorf("sender calls = %d, want 0 (no delivery attempt on interruption)", sender.calls)
			}
		})
	}
}

// TestWakeInterruptionWinsOverMissingInput proves the human-needed signal is
// not blocked by the usual target/text validation. A pending question must
// stop the wait even when the delivery fields are absent.
func TestWakeInterruptionWinsOverMissingInput(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	out, err := NewWakeHandler(sender).Wake(context.Background(), WakeInput{HasPendingQuestion: true})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if !out.Interrupted || out.Delivery != DeliveryInterrupted {
		t.Errorf("got interrupted=%v delivery=%q, want true/%q", out.Interrupted, out.Delivery, DeliveryInterrupted)
	}
	if out.Reason == "" {
		t.Error("reason is empty, want the interruption cause")
	}
	if sender.calls != 0 {
		t.Errorf("sender calls = %d, want 0 on interruption", sender.calls)
	}
}

// TestWakeSchemaHasHumanFlags proves the two ADR 0028 flags are part of the
// wake tool's input schema, so an MCP client can send them. The SDK reflects
// the schema from WakeInput; no hand-written JSON is involved.
func TestWakeSchemaHasHumanFlags(t *testing.T) {
	server := NewServer()
	AttachWake(server, NewWakeHandler(&fakeWakeSender{delivery: DeliveryQueue}))
	session := connectWakeServer(t, server)

	var inputSchema []byte
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		if tool.Name == WakeToolName {
			inputSchema, err = json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatalf("marshal wake input schema: %v", err)
			}
		}
	}
	if len(inputSchema) == 0 {
		t.Fatal("wake tool input schema is empty")
	}
	for _, flag := range []string{"hasPendingQuestion", "pendingApproval"} {
		if !strings.Contains(string(inputSchema), flag) {
			t.Errorf("wake input schema missing %q: %s", flag, inputSchema)
		}
	}
}

// TestWakeInterruptionOverInMemoryTransport drives a human flag through a real
// MCP client and server session. It proves the flag is decoded from the schema
// and the interruption reason reaches the client, not just the local handler.
func TestWakeInterruptionOverInMemoryTransport(t *testing.T) {
	server := NewServer()
	AttachWake(server, NewWakeHandler(&fakeWakeSender{delivery: DeliverySteer}))
	session := connectWakeServer(t, server)

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: WakeToolName,
		Arguments: WakeInput{
			Target:          "session-1",
			Text:            "obligation-1@7",
			PendingApproval: true,
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
	if !out.Interrupted || out.Delivery != DeliveryInterrupted {
		t.Errorf("client saw interrupted=%v delivery=%q, want true/%q", out.Interrupted, out.Delivery, DeliveryInterrupted)
	}
	if out.OK {
		t.Error("client saw ok=true on interruption, want false")
	}
	if !strings.Contains(out.Reason, "approval") {
		t.Errorf("client saw reason = %q, want it to mention approval", out.Reason)
	}
}

// TestWakeReviewSourceIsSteer is the T3.16 acceptance core (ADR 0030 p6): a
// diff comment has source review, travels through the steer channel and carries
// the source onto the output so the plugin can put it on the receipt.
func TestWakeReviewSourceIsSteer(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	out, err := NewWakeHandler(sender).Wake(context.Background(), WakeInput{
		Target: "session-1",
		Text:   "obligation-1@7",
		Source: SourceReview,
	})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if out.Delivery != DeliverySteer || !out.OK {
		t.Errorf("got delivery=%q ok=%v, want steer/true", out.Delivery, out.OK)
	}
	if out.Source != SourceReview {
		t.Errorf("source = %q, want %q", out.Source, SourceReview)
	}
	if sender.lastSource != SourceReview {
		t.Errorf("sender got source=%q, want %q", sender.lastSource, SourceReview)
	}
	if sender.lastText != "obligation-1@7" {
		t.Errorf("sender got text=%q, want the pointer unchanged", sender.lastText)
	}
}

// TestWakeSourceDefaultsToManual keeps callers written before ADR 0030 p6
// working: an omitted source is reported as manual, not as an error or review.
func TestWakeSourceDefaultsToManual(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	out, err := NewWakeHandler(sender).Wake(context.Background(), WakeInput{Target: "s", Text: "p"})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if out.Source != SourceManual || sender.lastSource != SourceManual {
		t.Errorf("got output source=%q sender source=%q, want both %q", out.Source, sender.lastSource, SourceManual)
	}
}

// TestWakeUnknownSourceIsRejected keeps the source field a closed set at the
// boundary. No delivery is attempted for a value nobody defined.
func TestWakeUnknownSourceIsRejected(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	if _, err := NewWakeHandler(sender).Wake(context.Background(), WakeInput{
		Target: "s",
		Text:   "p",
		Source: WakeSource("bogus"),
	}); err == nil {
		t.Fatal("Wake() error = nil, want an invalid-input error for an unknown source")
	}
	if sender.calls != 0 {
		t.Errorf("sender calls = %d, want 0 for an unknown source", sender.calls)
	}
}

// TestWakeReviewSourceDoesNotBreakInterruption proves the T2.24 flags still win
// over a review source: a human question interrupts before any delivery.
func TestWakeReviewSourceDoesNotBreakInterruption(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	out, err := NewWakeHandler(sender).Wake(context.Background(), WakeInput{
		Target:             "session-1",
		Text:               "obligation-1@7",
		Source:             SourceReview,
		HasPendingQuestion: true,
	})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if !out.Interrupted || out.Delivery != DeliveryInterrupted || out.OK {
		t.Errorf("got interrupted=%v delivery=%q ok=%v, want true/%q/false", out.Interrupted, out.Delivery, out.OK, DeliveryInterrupted)
	}
	if out.Source != SourceReview {
		t.Errorf("source = %q, want the review source on the interruption too", out.Source)
	}
	if sender.calls != 0 {
		t.Errorf("sender calls = %d, want 0 on interruption", sender.calls)
	}
}

// TestWakeSchemaHasSource proves an MCP client can send the message source,
// so a caller can mark a diff comment as review in the tool call.
func TestWakeSchemaHasSource(t *testing.T) {
	server := NewServer()
	AttachWake(server, NewWakeHandler(&fakeWakeSender{delivery: DeliveryQueue}))
	session := connectWakeServer(t, server)

	var inputSchema []byte
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		if tool.Name == WakeToolName {
			inputSchema, err = json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatalf("marshal wake input schema: %v", err)
			}
		}
	}
	if len(inputSchema) == 0 {
		t.Fatal("wake tool input schema is empty")
	}
	if !strings.Contains(string(inputSchema), "source") {
		t.Errorf("wake input schema missing source: %s", inputSchema)
	}
}

// TestWakeReviewOverInMemoryTransport drives a review-source diff comment
// through a real MCP client and server. It proves both the steer delivery and
// the source reach the client, not just the in-process handler.
func TestWakeReviewOverInMemoryTransport(t *testing.T) {
	server := NewServer()
	sender := &fakeWakeSender{delivery: DeliverySteer}
	AttachWake(server, NewWakeHandler(sender))
	session := connectWakeServer(t, server)

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: WakeToolName,
		Arguments: WakeInput{
			Target: "session-1",
			Text:   "obligation-1@7",
			Source: SourceReview,
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
	if out.Delivery != DeliverySteer || !out.OK || out.Source != SourceReview {
		t.Errorf("client saw delivery=%q ok=%v source=%q, want steer/true/review", out.Delivery, out.OK, out.Source)
	}
	if sender.lastSource != SourceReview {
		t.Errorf("sender got source=%q, want %q", sender.lastSource, SourceReview)
	}
}

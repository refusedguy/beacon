package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
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

// fakeAgentDirectory is an in-memory AgentDirectory. It records lookups and
// returns a fixed candidate set so tests can prove selection follows declared
// capabilities rather than names.
type fakeAgentDirectory struct {
	candidates []CandidateAgent
	calls      int
}

func (f *fakeAgentDirectory) CandidateAgents(_ context.Context) []CandidateAgent {
	f.calls++
	return f.candidates
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

// TestWakeEchoesExecutor proves the ADR 0031 p4 field reaches the output, so a
// CLI-manager handover after a 429 stays visible in the wake receipt.
func TestWakeEchoesExecutor(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	out, err := NewWakeHandler(sender).Wake(context.Background(), WakeInput{
		Target: "session-1", Text: "obligation-1@7", Executor: "hermes",
	})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if out.Executor != "hermes" {
		t.Errorf("executor = %q, want %q", out.Executor, "hermes")
	}
	if sender.lastText != "obligation-1@7" {
		t.Errorf("sender got text=%q, want the pointer unchanged", sender.lastText)
	}
}

// TestWakeExecutorOptional keeps older callers working: an omitted executor is
// empty on the output and does not turn into an invalid-input error.
func TestWakeExecutorOptional(t *testing.T) {
	out, err := NewWakeHandler(&fakeWakeSender{delivery: DeliveryQueue}).Wake(
		context.Background(), WakeInput{Target: "s", Text: "p"})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if out.Executor != "" {
		t.Errorf("executor = %q, want empty when caller omits it", out.Executor)
	}
}

// TestWakeExecutorOnRefusalAndInterruption keeps the executor visible on the
// honest paths too: an unsupported delivery and a human interruption.
func TestWakeExecutorOnRefusalAndInterruption(t *testing.T) {
	refusal, err := NewWakeHandler(&fakeWakeSender{delivery: DeliveryUnsupported, reason: "no channel"}).Wake(
		context.Background(), WakeInput{Target: "s", Text: "p", Executor: "gemini"})
	if err != nil {
		t.Fatalf("Wake() refusal error = %v", err)
	}
	if refusal.Executor != "gemini" || refusal.OK {
		t.Errorf("refusal executor=%q ok=%v, want gemini/false", refusal.Executor, refusal.OK)
	}

	interrupted, err := NewWakeHandler(&fakeWakeSender{delivery: DeliverySteer}).Wake(
		context.Background(), WakeInput{Target: "s", Text: "p", Executor: "gemini", HasPendingQuestion: true})
	if err != nil {
		t.Fatalf("Wake() interruption error = %v", err)
	}
	if interrupted.Executor != "gemini" || !interrupted.Interrupted {
		t.Errorf("interruption executor=%q interrupted=%v, want gemini/true",
			interrupted.Executor, interrupted.Interrupted)
	}
}

// TestWakeSchemaHasExecutor proves an MCP client can send the current executor,
// so a caller can report a fallback handover through the wake tool.
func TestWakeSchemaHasExecutor(t *testing.T) {
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
	if !strings.Contains(string(inputSchema), "executor") {
		t.Errorf("wake input schema missing executor: %s", inputSchema)
	}
}

// TestWakeExecutorOverInMemoryTransport drives the executor through a real MCP
// client and server. It proves the field is decoded from the schema and echoed
// back to the client, not just handled in process.
func TestWakeExecutorOverInMemoryTransport(t *testing.T) {
	server := NewServer()
	AttachWake(server, NewWakeHandler(&fakeWakeSender{delivery: DeliveryQueue}))
	session := connectWakeServer(t, server)

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: WakeToolName,
		Arguments: WakeInput{
			Target:   "session-1",
			Text:     "obligation-1@7",
			Executor: "hermes",
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
	if out.Executor != "hermes" {
		t.Errorf("client saw executor=%q, want %q", out.Executor, "hermes")
	}
}

// TestWokeIsNotDerivedFromDelivery is the T3.26 acceptance core (ADR 0032 p2):
// a dispatch receipt, successful or refused, never sets woke. Sending a
// follow-up is a different event from the agent starting work.
func TestWokeIsNotDerivedFromDelivery(t *testing.T) {
	cases := []struct {
		name     string
		delivery Delivery
	}{
		{name: "steer", delivery: DeliverySteer},
		{name: "queue", delivery: DeliveryQueue},
		{name: "unsupported", delivery: DeliveryUnsupported},
		{name: "unknown delivery", delivery: Delivery("bogus")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender := &fakeWakeSender{delivery: tc.delivery, reason: "no channel"}
			out, err := NewWakeHandler(sender).Wake(context.Background(), WakeInput{Target: "s", Text: "p"})
			if err != nil {
				t.Fatalf("Wake() error = %v", err)
			}
			if out.Woke {
				t.Error("woke = true from a dispatch alone; woke must be reported separately (ADR 0032 p2)")
			}
		})
	}
}

// TestWokeIsReportedIndependently proves a caller-supplied woke fact survives
// every delivery outcome, including a refusal and an interruption. It is not
// gated by OK and does not change the honest delivery result.
func TestWokeIsReportedIndependently(t *testing.T) {
	cases := []struct {
		name         string
		delivery     Delivery
		wantOK       bool
		wantDelivery Delivery
	}{
		{name: "steer", delivery: DeliverySteer, wantOK: true, wantDelivery: DeliverySteer},
		{name: "unsupported", delivery: DeliveryUnsupported, wantOK: false, wantDelivery: DeliveryUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender := &fakeWakeSender{delivery: tc.delivery, reason: "no channel"}
			out, err := NewWakeHandler(sender).Wake(context.Background(), WakeInput{Target: "s", Text: "p", Woke: true})
			if err != nil {
				t.Fatalf("Wake() error = %v", err)
			}
			if !out.Woke {
				t.Error("woke = false, want the caller's separate confirmation")
			}
			if out.OK != tc.wantOK || out.Delivery != tc.wantDelivery {
				t.Errorf("delivery=%q ok=%v, want %q/%v; woke must not alter delivery", out.Delivery, out.OK, tc.wantDelivery, tc.wantOK)
			}
		})
	}
}

// TestWokeOnInterruption keeps the fact independent of the ADR 0028 path too:
// a human interruption sends nothing, yet a reported start is still visible.
func TestWokeOnInterruption(t *testing.T) {
	out, err := NewWakeHandler(&fakeWakeSender{delivery: DeliverySteer}).Wake(
		context.Background(), WakeInput{Target: "s", Text: "p", HasPendingQuestion: true, Woke: true})
	if err != nil {
		t.Fatalf("Wake() error = %v", err)
	}
	if !out.Woke || !out.Interrupted || out.OK {
		t.Errorf("woke=%v interrupted=%v ok=%v, want true/true/false", out.Woke, out.Interrupted, out.OK)
	}
}

// TestWakeSchemaHasWoke proves an MCP client can send the separate woke fact.
func TestWakeSchemaHasWoke(t *testing.T) {
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
	if !strings.Contains(string(inputSchema), "woke") {
		t.Errorf("wake input schema missing woke: %s", inputSchema)
	}
}

// TestWokeOverInMemoryTransport drives woke through a real MCP client and
// server. It proves the fact is decoded from the schema and echoed back, not
// just handled in process.
func TestWokeOverInMemoryTransport(t *testing.T) {
	server := NewServer()
	AttachWake(server, NewWakeHandler(&fakeWakeSender{delivery: DeliveryQueue}))
	session := connectWakeServer(t, server)

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: WakeToolName,
		Arguments: WakeInput{
			Target: "session-1",
			Text:   "obligation-1@7",
			Woke:   true,
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
	if !out.Woke || !out.OK || out.Delivery != DeliveryQueue {
		t.Errorf("client saw woke=%v ok=%v delivery=%q, want true/true/queue", out.Woke, out.OK, out.Delivery)
	}
}

// TestSelectAgentByCapabilitiesRequiresAll proves the pure matcher only picks
// a session that declares every requested capability and never guesses when
// the required set is empty (ADR 0037 p1).
func TestSelectAgentByCapabilitiesRequiresAll(t *testing.T) {
	candidates := []CandidateAgent{
		{Target: "chat-only", Capabilities: []Capability{"chat"}},
		{Target: "has-both", Capabilities: []Capability{"review", "build"}},
	}
	if got, ok := selectAgentByCapabilities(candidates, []Capability{"review", "build"}); !ok || got != "has-both" {
		t.Errorf("select(review, build) = %q, %v; want has-both, true", got, ok)
	}
	if got, ok := selectAgentByCapabilities(candidates, []Capability{"deploy"}); ok || got != "" {
		t.Errorf("select(deploy) = %q, %v; want empty, false", got, ok)
	}
	if got, ok := selectAgentByCapabilities(candidates, nil); ok || got != "" {
		t.Errorf("select(nil) = %q, %v; want empty, false", got, ok)
	}
}

// TestWakeSelectsAgentByCapability is the T3.47 acceptance core: the caller
// asks for capabilities without naming a session, and wake delivers to the
// session that declares them.
func TestWakeSelectsAgentByCapability(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	directory := &fakeAgentDirectory{candidates: []CandidateAgent{
		{Target: "chat-session", Capabilities: []Capability{"chat"}},
		{Target: "review-session", Capabilities: []Capability{"review", "build"}},
	}}
	handler := NewCapabilityWakeHandler(sender, directory)

	out, err := handler.Wake(context.Background(), WakeInput{
		Text:                 "obligation-1@7",
		RequiredCapabilities: []Capability{"review"},
	})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if out.Delivery != DeliverySteer || !out.OK {
		t.Errorf("delivery=%q ok=%v, want steer/true", out.Delivery, out.OK)
	}
	if sender.calls != 1 || sender.lastTarget != "review-session" {
		t.Errorf("sender calls=%d target=%q, want 1 and review-session", sender.calls, sender.lastTarget)
	}
	if directory.calls != 1 {
		t.Errorf("directory calls=%d, want 1", directory.calls)
	}
}

// TestWakeCapabilityIsNotANameLookup proves a session name is a delivery key,
// not a selection criterion (ADR 0037 p1). The capable session wins whether or
// not the caller names a different session, and renaming it does not change the
// choice.
func TestWakeCapabilityIsNotANameLookup(t *testing.T) {
	directory := &fakeAgentDirectory{candidates: []CandidateAgent{
		{Target: "favourite-name", Capabilities: []Capability{"chat"}},
		{Target: "worker-7", Capabilities: []Capability{"review"}},
	}}
	sender := &fakeWakeSender{delivery: DeliveryQueue}
	first, err := NewCapabilityWakeHandler(sender, directory).Wake(context.Background(), WakeInput{
		Target:               "favourite-name",
		Text:                 "p",
		RequiredCapabilities: []Capability{"review"},
	})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if !first.OK || sender.lastTarget != "worker-7" {
		t.Errorf("got ok=%v target=%q, want true/worker-7", first.OK, sender.lastTarget)
	}

	directory.candidates[1].Target = "worker-99"
	renamedSender := &fakeWakeSender{delivery: DeliveryQueue}
	renamed, err := NewCapabilityWakeHandler(renamedSender, directory).Wake(context.Background(), WakeInput{
		Text:                 "p",
		RequiredCapabilities: []Capability{"review"},
	})
	if err != nil {
		t.Fatalf("Wake() after rename error = %v, want nil", err)
	}
	if !renamed.OK || renamedSender.lastTarget != "worker-99" {
		t.Errorf("after rename got ok=%v target=%q, want true/worker-99", renamed.OK, renamedSender.lastTarget)
	}
}

// TestWakeNoCapabilityMatchIsAnHonestRefusal keeps an unmatched capability
// request from becoming a delivery. The reason explains the refusal and no
// sender is touched.
func TestWakeNoCapabilityMatchIsAnHonestRefusal(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	directory := &fakeAgentDirectory{candidates: []CandidateAgent{
		{Target: "chat-session", Capabilities: []Capability{"chat"}},
	}}
	out, err := NewCapabilityWakeHandler(sender, directory).Wake(context.Background(), WakeInput{
		Text:                 "p",
		RequiredCapabilities: []Capability{"review"},
	})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil for an honest refusal", err)
	}
	if out.Delivery != DeliveryUnsupported || out.OK {
		t.Errorf("delivery=%q ok=%v, want unsupported/false", out.Delivery, out.OK)
	}
	if !strings.Contains(out.Reason, "capabilit") {
		t.Errorf("reason=%q, want it to name the missing capabilities", out.Reason)
	}
	if sender.calls != 0 {
		t.Errorf("sender calls=%d, want 0 when no session matches", sender.calls)
	}
}

// TestWakeCapabilityWithoutDirectoryIsUnsupported proves a capability request
// is never turned into a guessed name when the environment has no directory.
func TestWakeCapabilityWithoutDirectoryIsUnsupported(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	out, err := NewWakeHandler(sender).Wake(context.Background(), WakeInput{
		Text:                 "p",
		RequiredCapabilities: []Capability{"review"},
	})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if out.Delivery != DeliveryUnsupported || out.OK {
		t.Errorf("delivery=%q ok=%v, want unsupported/false", out.Delivery, out.OK)
	}
	if !strings.Contains(out.Reason, "directory") {
		t.Errorf("reason=%q, want it to name the missing directory", out.Reason)
	}
	if sender.calls != 0 {
		t.Errorf("sender calls=%d, want 0 without a directory", sender.calls)
	}
}

// TestWakeCapabilityKeepsHumanFlagsFirst guards T2.24: a pending question or
// approval interrupts before the directory is consulted or delivery attempted.
func TestWakeCapabilityKeepsHumanFlagsFirst(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	directory := &fakeAgentDirectory{candidates: []CandidateAgent{
		{Target: "review-session", Capabilities: []Capability{"review"}},
	}}
	out, err := NewCapabilityWakeHandler(sender, directory).Wake(context.Background(), WakeInput{
		Text:                 "p",
		HasPendingQuestion:   true,
		RequiredCapabilities: []Capability{"review"},
	})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if !out.Interrupted || out.Delivery != DeliveryInterrupted || out.OK {
		t.Errorf("got interrupted=%v delivery=%q ok=%v, want true/%q/false", out.Interrupted, out.Delivery, out.OK, DeliveryInterrupted)
	}
	if directory.calls != 0 || sender.calls != 0 {
		t.Errorf("directory calls=%d sender calls=%d, want 0/0 on interruption", directory.calls, sender.calls)
	}
}

// TestWakeCapabilityKeepsBothDeliveryModes guards T2.20: capability selection
// feeds the same two honest success modes as the explicit-target path.
func TestWakeCapabilityKeepsBothDeliveryModes(t *testing.T) {
	for _, delivery := range []Delivery{DeliverySteer, DeliveryQueue} {
		sender := &fakeWakeSender{delivery: delivery}
		directory := &fakeAgentDirectory{candidates: []CandidateAgent{
			{Target: "review-session", Capabilities: []Capability{"review"}},
		}}
		out, err := NewCapabilityWakeHandler(sender, directory).Wake(context.Background(), WakeInput{
			Text:                 "p",
			RequiredCapabilities: []Capability{"review"},
		})
		if err != nil {
			t.Fatalf("Wake(%q) error = %v, want nil", delivery, err)
		}
		if out.Delivery != delivery || !out.OK {
			t.Errorf("Wake(%q) delivery=%q ok=%v, want %q/true", delivery, out.Delivery, out.OK, delivery)
		}
		if out.Reason != "" {
			t.Errorf("Wake(%q) reason=%q, want empty on success", delivery, out.Reason)
		}
	}
}

// TestWakeWithoutCapabilitiesKeepsExplicitTarget keeps the path used before
// ADR 0037 p1: an explicit target is still the delivery key and no directory
// lookup happens.
func TestWakeWithoutCapabilitiesKeepsExplicitTarget(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliveryQueue}
	directory := &fakeAgentDirectory{candidates: []CandidateAgent{
		{Target: "capable-session", Capabilities: []Capability{"review"}},
	}}
	out, err := NewCapabilityWakeHandler(sender, directory).Wake(context.Background(), WakeInput{
		Target: "named-session",
		Text:   "p",
	})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if !out.OK || sender.lastTarget != "named-session" {
		t.Errorf("ok=%v target=%q, want true/named-session", out.OK, sender.lastTarget)
	}
	if directory.calls != 0 {
		t.Errorf("directory calls=%d, want 0 when no capabilities are requested", directory.calls)
	}
}

// TestWakeSchemaHasRequiredCapabilities proves an MCP client can send the
// capability request that drives the new selection path.
func TestWakeSchemaHasRequiredCapabilities(t *testing.T) {
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
	if !strings.Contains(string(inputSchema), "requiredCapabilities") {
		t.Errorf("wake input schema missing requiredCapabilities: %s", inputSchema)
	}
}

// TestWakeCapabilityOverInMemoryTransport drives capability selection through
// a real MCP client and server. It proves the request is decoded from the
// schema and the chosen target reaches the sender, not just the local handler.
func TestWakeCapabilityOverInMemoryTransport(t *testing.T) {
	server := NewServer()
	sender := &fakeWakeSender{delivery: DeliveryQueue}
	directory := &fakeAgentDirectory{candidates: []CandidateAgent{
		{Target: "chat-session", Capabilities: []Capability{"chat"}},
		{Target: "review-session", Capabilities: []Capability{"review"}},
	}}
	AttachWake(server, NewCapabilityWakeHandler(sender, directory))
	session := connectWakeServer(t, server)

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: WakeToolName,
		Arguments: WakeInput{
			Text:                 "p",
			RequiredCapabilities: []Capability{"review"},
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
	if sender.lastTarget != "review-session" {
		t.Errorf("sender target=%q, want review-session", sender.lastTarget)
	}
}

// TestSelectAgentByWorktree proves the pure matcher selects the session in the
// requested worktree and never picks a session from a different worktree
// (ADR 0042 p3). A candidate with no worktree path cannot be selected.
func TestSelectAgentByWorktree(t *testing.T) {
	candidates := []CandidateAgent{
		{Target: "session-w1", WorktreePath: "/repo/worktrees/w1"},
		{Target: "session-w2", WorktreePath: "/repo/worktrees/w2"},
		{Target: "no-worktree"},
	}
	if got, ok := selectAgentByWorktree(candidates, "/repo/worktrees/w2"); !ok || got != "session-w2" {
		t.Errorf("select(w2) = %q, %v; want session-w2, true", got, ok)
	}
	if got, ok := selectAgentByWorktree(candidates, "/repo/worktrees/w9"); ok || got != "" {
		t.Errorf("select(w9) = %q, %v; want empty, false", got, ok)
	}
	if got, ok := selectAgentByWorktree(candidates, ""); ok || got != "" {
		t.Errorf("select(empty) = %q, %v; want empty, false", got, ok)
	}
}

// TestSelectAgentCombinesRoutingKeys proves a caller can require both a
// capability set and a worktree path, and a candidate must satisfy both.
func TestSelectAgentCombinesRoutingKeys(t *testing.T) {
	candidates := []CandidateAgent{
		{Target: "caps-wrong-tree", Capabilities: []Capability{"review"}, WorktreePath: "/repo/w1"},
		{Target: "tree-wrong-caps", Capabilities: []Capability{"chat"}, WorktreePath: "/repo/w2"},
		{Target: "both-match", Capabilities: []Capability{"review", "build"}, WorktreePath: "/repo/w2"},
	}
	if got, ok := selectAgent(candidates, []Capability{"review"}, "/repo/w2"); !ok || got != "both-match" {
		t.Errorf("select(review, w2) = %q, %v; want both-match, true", got, ok)
	}
	if got, ok := selectAgent(candidates, []Capability{"review"}, "/repo/w9"); ok || got != "" {
		t.Errorf("select(review, w9) = %q, %v; want empty, false", got, ok)
	}
}

// TestWakeAddressesByWorktree is the T3.87 acceptance core: the caller names a
// worktree path, and wake delivers to the session running in that worktree
// even though the caller never names a session id (ADR 0042 p3).
func TestWakeAddressesByWorktree(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	directory := &fakeAgentDirectory{candidates: []CandidateAgent{
		{Target: "session-w1", WorktreePath: "/repo/worktrees/w1"},
		{Target: "session-w2", WorktreePath: "/repo/worktrees/w2"},
	}}
	handler := NewWorktreeWakeHandler(sender, directory)

	out, err := handler.Wake(context.Background(), WakeInput{
		Text:           "obligation-1@7",
		TargetWorktree: "/repo/worktrees/w2",
	})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if out.Delivery != DeliverySteer || !out.OK {
		t.Errorf("delivery=%q ok=%v, want steer/true", out.Delivery, out.OK)
	}
	if sender.calls != 1 || sender.lastTarget != "session-w2" {
		t.Errorf("sender calls=%d target=%q, want 1 and session-w2", sender.calls, sender.lastTarget)
	}
	if directory.calls != 1 {
		t.Errorf("directory calls=%d, want 1", directory.calls)
	}
}

// TestWakeWorktreeSurvivesRestart proves the worktree path is the stable key:
// when the environment reports a new session id in the same worktree after a
// restart, the next wake reaches the new session without reconfiguration
// (ADR 0042 p3).
func TestWakeWorktreeSurvivesRestart(t *testing.T) {
	directory := &fakeAgentDirectory{candidates: []CandidateAgent{
		{Target: "session-old", WorktreePath: "/repo/worktrees/w1"},
	}}
	in := WakeInput{Text: "p", TargetWorktree: "/repo/worktrees/w1"}

	firstSender := &fakeWakeSender{delivery: DeliveryQueue}
	if _, err := NewWorktreeWakeHandler(firstSender, directory).Wake(context.Background(), in); err != nil {
		t.Fatalf("Wake() before restart error = %v, want nil", err)
	}
	if firstSender.lastTarget != "session-old" {
		t.Errorf("target before restart = %q, want session-old", firstSender.lastTarget)
	}

	// Same worktree path, a different session id: a restart.
	directory.candidates[0].Target = "session-new"
	secondSender := &fakeWakeSender{delivery: DeliveryQueue}
	if _, err := NewWorktreeWakeHandler(secondSender, directory).Wake(context.Background(), in); err != nil {
		t.Fatalf("Wake() after restart error = %v, want nil", err)
	}
	if secondSender.lastTarget != "session-new" {
		t.Errorf("target after restart = %q, want session-new", secondSender.lastTarget)
	}
}

// TestWakeOtherWorktreeDoesNotReceive proves a session in a different worktree
// is not woken: with no candidate in the requested worktree the call is an
// honest refusal and no sender is touched (ADR 0042 p3).
func TestWakeOtherWorktreeDoesNotReceive(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	directory := &fakeAgentDirectory{candidates: []CandidateAgent{
		{Target: "session-w2", WorktreePath: "/repo/worktrees/w2"},
	}}
	out, err := NewWorktreeWakeHandler(sender, directory).Wake(context.Background(), WakeInput{
		Text: "p", TargetWorktree: "/repo/worktrees/w1",
	})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil for an honest refusal", err)
	}
	if out.Delivery != DeliveryUnsupported || out.OK {
		t.Errorf("delivery=%q ok=%v, want unsupported/false", out.Delivery, out.OK)
	}
	if !strings.Contains(out.Reason, "worktree") {
		t.Errorf("reason=%q, want it to name the worktree", out.Reason)
	}
	if sender.calls != 0 {
		t.Errorf("sender calls=%d, want 0 when no session is in the worktree", sender.calls)
	}
}

// TestWakeWorktreeWithoutDirectoryIsUnsupported proves a worktree request is
// never turned into a guessed session id when the environment has no directory.
func TestWakeWorktreeWithoutDirectoryIsUnsupported(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	out, err := NewWakeHandler(sender).Wake(context.Background(), WakeInput{
		Text: "p", TargetWorktree: "/repo/worktrees/w1",
	})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if out.Delivery != DeliveryUnsupported || out.OK {
		t.Errorf("delivery=%q ok=%v, want unsupported/false", out.Delivery, out.OK)
	}
	if !strings.Contains(out.Reason, "directory") {
		t.Errorf("reason=%q, want it to name the missing directory", out.Reason)
	}
	if sender.calls != 0 {
		t.Errorf("sender calls=%d, want 0 without a directory", sender.calls)
	}
}

// TestWakeWorktreeKeepsHumanFlagsFirst guards T2.24: a human flag interrupts
// before the worktree directory is consulted or any delivery is attempted.
func TestWakeWorktreeKeepsHumanFlagsFirst(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	directory := &fakeAgentDirectory{candidates: []CandidateAgent{
		{Target: "session-w1", WorktreePath: "/repo/worktrees/w1"},
	}}
	out, err := NewWorktreeWakeHandler(sender, directory).Wake(context.Background(), WakeInput{
		Text: "p", TargetWorktree: "/repo/worktrees/w1", HasPendingQuestion: true,
	})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if !out.Interrupted || out.Delivery != DeliveryInterrupted || out.OK {
		t.Errorf("got interrupted=%v delivery=%q ok=%v, want true/%q/false", out.Interrupted, out.Delivery, out.OK, DeliveryInterrupted)
	}
	if directory.calls != 0 || sender.calls != 0 {
		t.Errorf("directory calls=%d sender calls=%d, want 0/0 on interruption", directory.calls, sender.calls)
	}
}

// TestWakeWorktreeKeepsBothDeliveryModes guards T2.20: worktree addressing
// feeds the same two honest success modes as the explicit-target path.
func TestWakeWorktreeKeepsBothDeliveryModes(t *testing.T) {
	for _, delivery := range []Delivery{DeliverySteer, DeliveryQueue} {
		sender := &fakeWakeSender{delivery: delivery}
		directory := &fakeAgentDirectory{candidates: []CandidateAgent{
			{Target: "session-w1", WorktreePath: "/repo/worktrees/w1"},
		}}
		out, err := NewWorktreeWakeHandler(sender, directory).Wake(context.Background(), WakeInput{
			Text: "p", TargetWorktree: "/repo/worktrees/w1",
		})
		if err != nil {
			t.Fatalf("Wake(%q) error = %v, want nil", delivery, err)
		}
		if out.Delivery != delivery || !out.OK {
			t.Errorf("Wake(%q) delivery=%q ok=%v, want %q/true", delivery, out.Delivery, out.OK, delivery)
		}
	}
}

// TestWakeWorktreeKeepsExplicitTarget proves an explicit Target caller (no
// worktree path) keeps the pre-ADR-0042 path: no directory lookup happens.
func TestWakeWorktreeKeepsExplicitTarget(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliveryQueue}
	directory := &fakeAgentDirectory{candidates: []CandidateAgent{
		{Target: "session-w1", WorktreePath: "/repo/worktrees/w1"},
	}}
	out, err := NewWorktreeWakeHandler(sender, directory).Wake(context.Background(), WakeInput{
		Target: "named-session", Text: "p",
	})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if !out.OK || sender.lastTarget != "named-session" {
		t.Errorf("ok=%v target=%q, want true/named-session", out.OK, sender.lastTarget)
	}
	if directory.calls != 0 {
		t.Errorf("directory calls=%d, want 0 without a routing key", directory.calls)
	}
}

// TestWakeWorktreeWithCapabilities proves capability and worktree routing
// compose: only a session that declares the capability and runs in the
// requested worktree is chosen.
func TestWakeWorktreeWithCapabilities(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	directory := &fakeAgentDirectory{candidates: []CandidateAgent{
		{Target: "review-w1", Capabilities: []Capability{"review"}, WorktreePath: "/repo/worktrees/w1"},
		{Target: "review-w2", Capabilities: []Capability{"review"}, WorktreePath: "/repo/worktrees/w2"},
	}}
	out, err := NewCapabilityWakeHandler(sender, directory).Wake(context.Background(), WakeInput{
		Text:                 "p",
		RequiredCapabilities: []Capability{"review"},
		TargetWorktree:       "/repo/worktrees/w2",
	})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if !out.OK || sender.lastTarget != "review-w2" {
		t.Errorf("ok=%v target=%q, want true/review-w2", out.OK, sender.lastTarget)
	}
}

// TestWakeSchemaHasTargetWorktree proves an MCP client can send the worktree
// path that drives the new addressing path.
func TestWakeSchemaHasTargetWorktree(t *testing.T) {
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
	if !strings.Contains(string(inputSchema), "targetWorktree") {
		t.Errorf("wake input schema missing targetWorktree: %s", inputSchema)
	}
}

// TestWakeWorktreeOverInMemoryTransport drives worktree addressing through a
// real MCP client and server. It proves the path is decoded from the schema
// and the session in that worktree receives the delivery.
func TestWakeWorktreeOverInMemoryTransport(t *testing.T) {
	server := NewServer()
	sender := &fakeWakeSender{delivery: DeliveryQueue}
	directory := &fakeAgentDirectory{candidates: []CandidateAgent{
		{Target: "session-w1", WorktreePath: "/repo/worktrees/w1"},
		{Target: "session-w2", WorktreePath: "/repo/worktrees/w2"},
	}}
	AttachWake(server, NewWorktreeWakeHandler(sender, directory))
	session := connectWakeServer(t, server)

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: WakeToolName,
		Arguments: WakeInput{
			Text:           "p",
			TargetWorktree: "/repo/worktrees/w2",
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
	if sender.lastTarget != "session-w2" {
		t.Errorf("sender target=%q, want session-w2", sender.lastTarget)
	}
}

// fallbackAttempt records one channel attempt of the ADR 0038 p6 chain. The
// channel is the hint the core asked for; the delivery is what the adapter
// reports actually happened.
type fallbackAttempt struct {
	channel WakeChannel
	target  string
	text    string
	source  WakeSource
}

// channelResult is the canned answer of fakeFallbackSender for one channel.
type channelResult struct {
	delivery Delivery
	reason   string
}

// fakeFallbackSender answers each channel from a map and records the order in
// which the core tried them.
type fakeFallbackSender struct {
	results  map[WakeChannel]channelResult
	attempts []fallbackAttempt
}

func (f *fakeFallbackSender) Deliver(_ context.Context, target, text string, source WakeSource, channel WakeChannel) (Delivery, string) {
	f.attempts = append(f.attempts, fallbackAttempt{channel: channel, target: target, text: text, source: source})
	if result, ok := f.results[channel]; ok {
		return result.delivery, result.reason
	}
	return DeliveryUnsupported, ""
}

// fakeConfirmer reports confirmation per channel and records the order in
// which channels were checked.
type fakeConfirmer struct {
	confirmed map[WakeChannel]bool
	calls     []WakeChannel
}

func (f *fakeConfirmer) Confirmed(_ context.Context, _ string, _ string, channel WakeChannel) bool {
	f.calls = append(f.calls, channel)
	return f.confirmed[channel]
}

// fakeTracer records the last-resort task trace write of the fallback chain.
type fakeTracer struct {
	calls      int
	lastTarget string
	lastText   string
	lastSource WakeSource
	err        error
}

func (f *fakeTracer) Trace(_ context.Context, target, text string, source WakeSource) error {
	f.calls++
	f.lastTarget = target
	f.lastText = text
	f.lastSource = source
	return f.err
}

// TestWakeFallbackConfirmedSteerStops is the first rung of the ADR 0038 p6
// chain: a confirmed delivery into the running turn wins and no further
// channel is touched.
func TestWakeFallbackConfirmedSteerStops(t *testing.T) {
	sender := &fakeFallbackSender{results: map[WakeChannel]channelResult{
		ChannelSteer: {delivery: DeliverySteer},
		ChannelQueue: {delivery: DeliveryQueue},
	}}
	confirmer := &fakeConfirmer{confirmed: map[WakeChannel]bool{ChannelSteer: true, ChannelQueue: true}}
	tracer := &fakeTracer{}

	out, err := NewFallbackWakeHandler(sender, confirmer, tracer).WakeWithFallback(
		context.Background(), WakeInput{Target: "session-1", Text: "obligation-1@7"})
	if err != nil {
		t.Fatalf("WakeWithFallback() error = %v, want nil", err)
	}
	if out.Delivery != DeliverySteer || !out.OK || out.Fallback != FallbackSteer {
		t.Errorf("got delivery=%q ok=%v fallback=%q, want steer/true/steer", out.Delivery, out.OK, out.Fallback)
	}
	if out.Traced {
		t.Error("traced = true on a confirmed steer delivery, want false")
	}
	if len(sender.attempts) != 1 || sender.attempts[0].channel != ChannelSteer {
		t.Errorf("attempts = %+v, want only steer", sender.attempts)
	}
	if tracer.calls != 0 {
		t.Errorf("tracer calls = %d, want 0 when a channel was confirmed", tracer.calls)
	}
}

// TestWakeFallbackUnconfirmedSteerGoesToNextTurn is the T3.58 acceptance core:
// an unconfirmed running-turn delivery falls through to the next turn, and the
// honest delivery becomes queue when that channel is confirmed.
func TestWakeFallbackUnconfirmedSteerGoesToNextTurn(t *testing.T) {
	sender := &fakeFallbackSender{results: map[WakeChannel]channelResult{
		ChannelSteer: {delivery: DeliverySteer},
		ChannelQueue: {delivery: DeliveryQueue},
	}}
	confirmer := &fakeConfirmer{confirmed: map[WakeChannel]bool{ChannelQueue: true}}
	tracer := &fakeTracer{}

	out, err := NewFallbackWakeHandler(sender, confirmer, tracer).WakeWithFallback(
		context.Background(), WakeInput{Target: "session-1", Text: "obligation-1@7"})
	if err != nil {
		t.Fatalf("WakeWithFallback() error = %v, want nil", err)
	}
	if out.Delivery != DeliveryQueue || !out.OK || out.Fallback != FallbackQueue {
		t.Errorf("got delivery=%q ok=%v fallback=%q, want queue/true/queue", out.Delivery, out.OK, out.Fallback)
	}
	if got := channelsOf(sender.attempts); len(got) != 2 || got[0] != ChannelSteer || got[1] != ChannelQueue {
		t.Errorf("attempt channels = %v, want [steer queue]", got)
	}
	if got := confirmer.calls; len(got) != 2 || got[0] != ChannelSteer || got[1] != ChannelQueue {
		t.Errorf("confirm check order = %v, want [steer queue]", got)
	}
	if tracer.calls != 0 {
		t.Errorf("tracer calls = %d, want 0 when queue was confirmed", tracer.calls)
	}
}

// TestWakeFallbackExhaustedGoesToTaskTrace proves the last rung: with no
// confirmation anywhere the message is written to the task trace, and the
// honest delivery stays unsupported with OK=false.
func TestWakeFallbackExhaustedGoesToTaskTrace(t *testing.T) {
	sender := &fakeFallbackSender{results: map[WakeChannel]channelResult{
		ChannelSteer: {delivery: DeliverySteer},
		ChannelQueue: {delivery: DeliveryQueue},
	}}
	confirmer := &fakeConfirmer{confirmed: map[WakeChannel]bool{}}
	tracer := &fakeTracer{}

	out, err := NewFallbackWakeHandler(sender, confirmer, tracer).WakeWithFallback(
		context.Background(), WakeInput{Target: "session-1", Text: "obligation-1@7", Source: SourceReview})
	if err != nil {
		t.Fatalf("WakeWithFallback() error = %v, want nil", err)
	}
	if out.Delivery != DeliveryUnsupported || out.OK {
		t.Errorf("delivery=%q ok=%v, want unsupported/false on the trace path", out.Delivery, out.OK)
	}
	if out.Fallback != FallbackTraced || !out.Traced {
		t.Errorf("fallback=%q traced=%v, want traced/true", out.Fallback, out.Traced)
	}
	if out.Source != SourceReview || out.Reason == "" {
		t.Errorf("source=%q reason=%q, want review and a trace explanation", out.Source, out.Reason)
	}
	if !strings.Contains(out.Reason, "task trace") {
		t.Errorf("reason=%q, want it to name the task trace", out.Reason)
	}
	if tracer.calls != 1 || tracer.lastTarget != "session-1" || tracer.lastText != "obligation-1@7" || tracer.lastSource != SourceReview {
		t.Errorf("tracer got calls=%d target=%q text=%q source=%q", tracer.calls, tracer.lastTarget, tracer.lastText, tracer.lastSource)
	}
}

// TestWakeFallbackUnsupportedIsNotSuccess keeps ADR 0026 honest under fallback:
// a chain that never reaches a supported channel must still answer unsupported,
// and the task trace must not be counted as a delivery.
func TestWakeFallbackUnsupportedIsNotSuccess(t *testing.T) {
	sender := &fakeFallbackSender{results: map[WakeChannel]channelResult{
		ChannelSteer: {delivery: DeliveryUnsupported, reason: "no running turn"},
		ChannelQueue: {delivery: DeliveryUnsupported, reason: "no queue channel"},
	}}
	confirmer := &fakeConfirmer{confirmed: map[WakeChannel]bool{ChannelSteer: true, ChannelQueue: true}}
	tracer := &fakeTracer{}

	out, err := NewFallbackWakeHandler(sender, confirmer, tracer).WakeWithFallback(
		context.Background(), WakeInput{Target: "s", Text: "p"})
	if err != nil {
		t.Fatalf("WakeWithFallback() error = %v, want nil", err)
	}
	if out.Delivery != DeliveryUnsupported || out.OK || !out.Traced {
		t.Errorf("delivery=%q ok=%v traced=%v, want unsupported/false/true", out.Delivery, out.OK, out.Traced)
	}
	for _, part := range []string{"no running turn", "no queue channel"} {
		if !strings.Contains(out.Reason, part) {
			t.Errorf("reason=%q, want it to keep %q", out.Reason, part)
		}
	}
	if len(confirmer.calls) != 0 {
		t.Errorf("confirm checks = %v, want none for unsupported channels", confirmer.calls)
	}
}

// TestWakeFallbackWithoutConfirmerIsTraced keeps silence honest: without a
// confirmer there is no receipt, so both channels are attempted and the
// message ends in the task trace rather than being reported as delivered.
func TestWakeFallbackWithoutConfirmerIsTraced(t *testing.T) {
	sender := &fakeFallbackSender{results: map[WakeChannel]channelResult{
		ChannelSteer: {delivery: DeliverySteer},
		ChannelQueue: {delivery: DeliveryQueue},
	}}
	tracer := &fakeTracer{}

	out, err := NewFallbackWakeHandler(sender, nil, tracer).WakeWithFallback(
		context.Background(), WakeInput{Target: "s", Text: "p"})
	if err != nil {
		t.Fatalf("WakeWithFallback() error = %v, want nil", err)
	}
	if out.Delivery != DeliveryUnsupported || out.OK || !out.Traced {
		t.Errorf("delivery=%q ok=%v traced=%v, want unsupported/false/true", out.Delivery, out.OK, out.Traced)
	}
}

// TestWakeFallbackTracerFailureIsHonest proves a failed trace write is not
// mistaken for a recorded follow-up: Traced stays false and the reason names
// the failure.
func TestWakeFallbackTracerFailureIsHonest(t *testing.T) {
	sender := &fakeFallbackSender{results: map[WakeChannel]channelResult{
		ChannelSteer: {delivery: DeliverySteer},
		ChannelQueue: {delivery: DeliveryQueue},
	}}
	tracer := &fakeTracer{err: errors.New("store unavailable")}

	out, err := NewFallbackWakeHandler(sender, &fakeConfirmer{}, tracer).WakeWithFallback(
		context.Background(), WakeInput{Target: "s", Text: "p"})
	if err != nil {
		t.Fatalf("WakeWithFallback() error = %v, want nil", err)
	}
	if out.Delivery != DeliveryUnsupported || out.OK || out.Traced {
		t.Errorf("delivery=%q ok=%v traced=%v, want unsupported/false/false", out.Delivery, out.OK, out.Traced)
	}
	if !strings.Contains(out.Reason, "store unavailable") {
		t.Errorf("reason=%q, want it to name the trace failure", out.Reason)
	}
}

// TestWakeFallbackKeepsHumanFlagsFirst guards T2.24: a pending question or
// approval interrupts before any channel attempt or trace write.
func TestWakeFallbackKeepsHumanFlagsFirst(t *testing.T) {
	sender := &fakeFallbackSender{results: map[WakeChannel]channelResult{
		ChannelSteer: {delivery: DeliverySteer},
		ChannelQueue: {delivery: DeliveryQueue},
	}}
	confirmer := &fakeConfirmer{confirmed: map[WakeChannel]bool{ChannelSteer: true, ChannelQueue: true}}
	tracer := &fakeTracer{}

	out, err := NewFallbackWakeHandler(sender, confirmer, tracer).WakeWithFallback(
		context.Background(), WakeInput{Target: "s", Text: "p", HasPendingQuestion: true})
	if err != nil {
		t.Fatalf("WakeWithFallback() error = %v, want nil", err)
	}
	if !out.Interrupted || out.Delivery != DeliveryInterrupted || out.OK {
		t.Errorf("got interrupted=%v delivery=%q ok=%v, want true/%q/false", out.Interrupted, out.Delivery, out.OK, DeliveryInterrupted)
	}
	if len(sender.attempts) != 0 || len(confirmer.calls) != 0 || tracer.calls != 0 {
		t.Errorf("attempts=%v confirm=%v traces=%d, want 0/0/0 on interruption", sender.attempts, confirmer.calls, tracer.calls)
	}
}

// TestWakeFallbackKeepsCapabilitySelection guards T3.47: the capability
// directory still picks the target, and the chosen session is the one the
// fallback chain delivers to.
func TestWakeFallbackKeepsCapabilitySelection(t *testing.T) {
	sender := &fakeFallbackSender{results: map[WakeChannel]channelResult{
		ChannelSteer: {delivery: DeliverySteer},
	}}
	confirmer := &fakeConfirmer{confirmed: map[WakeChannel]bool{ChannelSteer: true}}
	directory := &fakeAgentDirectory{candidates: []CandidateAgent{
		{Target: "chat-session", Capabilities: []Capability{"chat"}},
		{Target: "review-session", Capabilities: []Capability{"review"}},
	}}
	handler := NewCapabilityFallbackWakeHandler(sender, confirmer, &fakeTracer{}, directory)

	out, err := handler.WakeWithFallback(context.Background(), WakeInput{
		Text:                 "p",
		RequiredCapabilities: []Capability{"review"},
	})
	if err != nil {
		t.Fatalf("WakeWithFallback() error = %v, want nil", err)
	}
	if !out.OK || out.Delivery != DeliverySteer {
		t.Errorf("delivery=%q ok=%v, want steer/true", out.Delivery, out.OK)
	}
	if len(sender.attempts) != 1 || sender.attempts[0].target != "review-session" {
		t.Errorf("attempts=%+v, want steer to review-session", sender.attempts)
	}
}

// TestWakeFallbackKeepsWorktreeSelection guards T3.58 together with T3.87:
// the worktree lookup picks the target, and the chosen session is the one the
// fallback chain delivers to.
func TestWakeFallbackKeepsWorktreeSelection(t *testing.T) {
	sender := &fakeFallbackSender{results: map[WakeChannel]channelResult{
		ChannelSteer: {delivery: DeliverySteer},
	}}
	confirmer := &fakeConfirmer{confirmed: map[WakeChannel]bool{ChannelSteer: true}}
	directory := &fakeAgentDirectory{candidates: []CandidateAgent{
		{Target: "session-w1", WorktreePath: "/repo/worktrees/w1"},
		{Target: "session-w2", WorktreePath: "/repo/worktrees/w2"},
	}}
	handler := NewCapabilityFallbackWakeHandler(sender, confirmer, &fakeTracer{}, directory)

	out, err := handler.WakeWithFallback(context.Background(), WakeInput{
		Text: "p", TargetWorktree: "/repo/worktrees/w2",
	})
	if err != nil {
		t.Fatalf("WakeWithFallback() error = %v, want nil", err)
	}
	if !out.OK || out.Delivery != DeliverySteer {
		t.Errorf("delivery=%q ok=%v, want steer/true", out.Delivery, out.OK)
	}
	if len(sender.attempts) != 1 || sender.attempts[0].target != "session-w2" {
		t.Errorf("attempts=%+v, want steer to session-w2", sender.attempts)
	}
}

// TestWakeFallbackOverInMemoryTransport drives the chain through a real MCP
// client and server. It proves the honest delivery and the separate traced
// fact reach the client, not just the in-process handler.
func TestWakeFallbackOverInMemoryTransport(t *testing.T) {
	server := NewServer()
	sender := &fakeFallbackSender{results: map[WakeChannel]channelResult{
		ChannelSteer: {delivery: DeliverySteer},
		ChannelQueue: {delivery: DeliveryQueue},
	}}
	AttachWake(server, NewFallbackWakeHandler(sender, &fakeConfirmer{}, &fakeTracer{}))
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
	if out.Delivery != DeliveryUnsupported || out.OK || !out.Traced || out.Fallback != FallbackTraced {
		t.Errorf("client saw delivery=%q ok=%v traced=%v fallback=%q, want unsupported/false/true/traced",
			out.Delivery, out.OK, out.Traced, out.Fallback)
	}
}

// channelsOf extracts the requested channels in attempt order.
func channelsOf(attempts []fallbackAttempt) []WakeChannel {
	channels := make([]WakeChannel, 0, len(attempts))
	for _, attempt := range attempts {
		channels = append(channels, attempt.channel)
	}
	return channels
}

// recordingSleeper captures each wake point instead of sleeping, so a test can
// observe staggered wake times without making CI wait.
type recordingSleeper struct {
	points []time.Duration
	err    error
}

func (r *recordingSleeper) Sleep(_ context.Context, d time.Duration) error {
	r.points = append(r.points, d)
	return r.err
}

// TestWakeWithoutWindowDoesNotWait keeps every caller written before ADR 0041
// on the immediate path. No window means the sleeper is never touched.
func TestWakeWithoutWindowDoesNotWait(t *testing.T) {
	for _, millis := range []int64{0, -1} {
		sleeper := &recordingSleeper{}
		sender := &fakeWakeSender{delivery: DeliverySteer}
		handler := NewWakeHandler(sender).WithWakeSleeper(sleeper)

		out, err := handler.Wake(context.Background(), WakeInput{
			Target: "s", Text: "p", WakeBetweenMillis: millis,
		})
		if err != nil || !out.OK {
			t.Fatalf("Wake(millis=%d) error=%v out=%+v, want a delivered wake", millis, err, out)
		}
		if len(sleeper.points) != 0 {
			t.Errorf("Wake(millis=%d) sleeper points=%v, want no wait", millis, sleeper.points)
		}
	}
}

// TestWakeBetweenStaggersFifteenWaiters is the ADR 0041 p5 acceptance core:
// fifteen agents waiting on one event must wake at spread points in the
// window, not all at the same instant. The recording sleeper holds the point
// in time each waiter chose.
func TestWakeBetweenStaggersFifteenWaiters(t *testing.T) {
	const waiters = 15
	const windowMillis = int64(1500)
	window := time.Duration(windowMillis) * time.Millisecond
	sleeper := &recordingSleeper{}
	handler := NewWakeHandler(&fakeWakeSender{delivery: DeliverySteer}).WithWakeSleeper(sleeper)

	for i := 0; i < waiters; i++ {
		out, err := handler.Wake(context.Background(), WakeInput{
			Target: "session-1", Text: "obligation-1@7", WakeBetweenMillis: windowMillis,
		})
		if err != nil || !out.OK {
			t.Fatalf("waiter %d: error=%v out=%+v, want a delivered wake", i, err, out)
		}
	}

	if len(sleeper.points) != waiters {
		t.Fatalf("sleeper points=%d, want %d", len(sleeper.points), waiters)
	}
	seen := make(map[time.Duration]bool, waiters)
	var min, max time.Duration
	for i, point := range sleeper.points {
		if point < 0 || point >= window {
			t.Errorf("waiter %d point=%v, want inside [0,%v)", i, point, window)
		}
		if seen[point] {
			t.Errorf("waiter %d point=%v repeats an earlier waiter: waiters woke at the same instant", i, point)
		}
		seen[point] = true
		if i == 0 || point < min {
			min = point
		}
		if i == 0 || point > max {
			max = point
		}
	}
	if max-min < window/4 {
		t.Errorf("wake points span only %v inside a %v window, want a visible spread", max-min, window)
	}
}

// TestRandomWakePointSpreadsAcrossWindow checks the real uniform picker with a
// large sample. Loose bounds keep the test stable in CI while still failing a
// picker that returns a constant or clusters in one part of the window.
func TestRandomWakePointSpreadsAcrossWindow(t *testing.T) {
	const samples = 400
	window := 2 * time.Second
	buckets := make([]int, 10)
	seen := make(map[time.Duration]bool, samples)
	for i := 0; i < samples; i++ {
		point := randomWakePoint(window)
		if point < 0 || point >= window {
			t.Fatalf("point %v outside [0,%v)", point, window)
		}
		seen[point] = true
		bucket := int(int64(point) * int64(len(buckets)) / int64(window))
		buckets[bucket]++
	}
	unique := 0
	for _, count := range buckets {
		if count > 0 {
			unique++
		}
	}
	if unique != len(buckets) {
		t.Errorf("picker covered %d of %d window slices, want all; buckets=%v", unique, len(buckets), buckets)
	}
	if len(seen) < samples/2 {
		t.Errorf("picker returned only %d distinct points of %d samples", len(seen), samples)
	}
	if buckets[0] == 0 || buckets[len(buckets)-1] == 0 {
		t.Errorf("picker never reached the window edges; buckets=%v", buckets)
	}
}

// TestWakeBetweenUsesWindowFromContract proves the delay comes from the call's
// window, with the chosen point handed to the sleeper before any delivery.
func TestWakeBetweenUsesWindowFromContract(t *testing.T) {
	window := 900 * time.Millisecond
	sleeper := &recordingSleeper{}
	sender := &fakeWakeSender{delivery: DeliveryQueue}
	handler := NewWakeHandler(sender).
		WithWakeSleeper(sleeper).
		WithWakePoint(func(got time.Duration) time.Duration {
			if got != window {
				t.Errorf("point picker window=%v, want %v from the wait contract", got, window)
			}
			return window / 3
		})

	out, err := handler.Wake(context.Background(), WakeInput{
		Target: "s", Text: "p", WakeBetweenMillis: 900,
	})
	if err != nil || !out.OK {
		t.Fatalf("Wake() error=%v out=%+v, want a delivered wake", err, out)
	}
	if len(sleeper.points) != 1 || sleeper.points[0] != window/3 {
		t.Errorf("sleeper points=%v, want one point of %v", sleeper.points, window/3)
	}
	if sender.calls != 1 {
		t.Errorf("sender calls=%d, want 1", sender.calls)
	}
}

// TestWakeBetweenHumanFlagsWin keeps T2.24: a waiter that needs a human is
// interrupted before it waits for a random point in the window.
func TestWakeBetweenHumanFlagsWin(t *testing.T) {
	sleeper := &recordingSleeper{}
	sender := &fakeWakeSender{delivery: DeliverySteer}
	out, err := NewWakeHandler(sender).WithWakeSleeper(sleeper).Wake(
		context.Background(), WakeInput{
			Target: "s", Text: "p", WakeBetweenMillis: 1500, HasPendingQuestion: true,
		})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if !out.Interrupted || out.OK {
		t.Errorf("interrupted=%v ok=%v, want true/false", out.Interrupted, out.OK)
	}
	if len(sleeper.points) != 0 || sender.calls != 0 {
		t.Errorf("sleeper points=%v sender calls=%d, want 0/0 on interruption", sleeper.points, sender.calls)
	}
}

// TestWakeBetweenCancelledWaitDeliversNothing proves a cancelled wait is an
// error, never a delivery after the caller has gone away.
func TestWakeBetweenCancelledWaitDeliversNothing(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	handler := NewWakeHandler(sender).WithWakeSleeper(&recordingSleeper{err: context.Canceled})

	_, err := handler.Wake(context.Background(), WakeInput{
		Target: "s", Text: "p", WakeBetweenMillis: 1500,
	})
	if err == nil {
		t.Fatal("Wake() error = nil, want a cancellation error")
	}
	var appErr *domain.AppError
	if !errors.As(err, &appErr) || appErr.Code != domain.CodeCancelled {
		t.Errorf("error = %v, want an AppError with code %q", err, domain.CodeCancelled)
	}
	if sender.calls != 0 {
		t.Errorf("sender calls=%d, want 0 on a cancelled wait", sender.calls)
	}
}

// TestWakeBetweenFallbackStaggersOnce keeps T3.58: the fallback chain still
// runs, and every channel of one waiter shares a single wake point.
func TestWakeBetweenFallbackStaggersOnce(t *testing.T) {
	sender := &fakeFallbackSender{results: map[WakeChannel]channelResult{
		ChannelSteer: {delivery: DeliverySteer},
		ChannelQueue: {delivery: DeliveryQueue},
	}}
	sleeper := &recordingSleeper{}
	confirmer := &fakeConfirmer{confirmed: map[WakeChannel]bool{ChannelQueue: true}}
	handler := NewFallbackWakeHandler(sender, confirmer, &fakeTracer{}).WithWakeSleeper(sleeper)

	out, err := handler.WakeWithFallback(context.Background(), WakeInput{
		Target: "s", Text: "p", WakeBetweenMillis: 900,
	})
	if err != nil {
		t.Fatalf("WakeWithFallback() error = %v, want nil", err)
	}
	if out.Delivery != DeliveryQueue || !out.OK {
		t.Errorf("delivery=%q ok=%v, want queue/true", out.Delivery, out.OK)
	}
	if len(sleeper.points) != 1 {
		t.Errorf("sleeper points=%d, want one stagger for the whole chain", len(sleeper.points))
	}
	if got := channelsOf(sender.attempts); len(got) != 2 || got[0] != ChannelSteer || got[1] != ChannelQueue {
		t.Errorf("attempt channels=%v, want [steer queue]", got)
	}
}

// TestWakeSchemaHasWakeBetweenMillis proves an MCP client can send the wait
// window that drives the stagger.
func TestWakeSchemaHasWakeBetweenMillis(t *testing.T) {
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
	if !strings.Contains(string(inputSchema), "wakeBetweenMillis") {
		t.Errorf("wake input schema missing wakeBetweenMillis: %s", inputSchema)
	}
}

// TestWakeBetweenOverInMemoryTransport drives the window through a real MCP
// client and server. It proves the field is decoded from the schema and the
// waiter is staggered before delivery.
func TestWakeBetweenOverInMemoryTransport(t *testing.T) {
	server := NewServer()
	sleeper := &recordingSleeper{}
	AttachWake(server, NewWakeHandler(&fakeWakeSender{delivery: DeliveryQueue}).WithWakeSleeper(sleeper))
	session := connectWakeServer(t, server)

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: WakeToolName,
		Arguments: WakeInput{
			Target: "session-1", Text: "obligation-1@7", WakeBetweenMillis: 900,
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
	if len(sleeper.points) != 1 || sleeper.points[0] < 0 || sleeper.points[0] >= 900*time.Millisecond {
		t.Errorf("sleeper points=%v, want one point inside the 900ms window", sleeper.points)
	}
}

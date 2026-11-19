package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
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

// TestWakeReceiptStateIsClosedSet is the ADR 0043 feature 4 acceptance core:
// a receipt carries one of eight distinct lifecycle states and the ADR 0026
// delivery values are not among them. State answers where the message is;
// delivery answers how it was sent.
func TestWakeReceiptStateIsClosedSet(t *testing.T) {
	states := []DeliveryState{
		StateSpooled, StatePending, StateHeld, StatePushed,
		StatePushUnreachable, StateRead, StateRefused, StateExpired,
	}
	if len(states) != 8 {
		t.Fatalf("got %d receipt states, want 8", len(states))
	}
	seen := make(map[DeliveryState]bool, len(states))
	for _, state := range states {
		if !validDeliveryState(state) {
			t.Errorf("state %q is not accepted by validDeliveryState", state)
		}
		if seen[state] {
			t.Errorf("state %q is duplicated", state)
		}
		seen[state] = true
	}
	for _, delivery := range []Delivery{DeliverySteer, DeliveryQueue, DeliveryUnsupported} {
		if validDeliveryState(DeliveryState(delivery)) {
			t.Errorf("delivery %q must not be a receipt state", delivery)
		}
	}
}

// TestWakeUnknownReceiptStateRejected keeps the state field a closed set at the
// boundary. An invented state is an invalid input and touches no sender.
func TestWakeUnknownReceiptStateRejected(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	out, err := NewWakeHandler(sender).Wake(context.Background(), WakeInput{
		Target: "s", Text: "p", State: DeliveryState("bogus"),
	})
	if err == nil {
		t.Fatal("Wake() error = nil, want an invalid-input error for an unknown receipt state")
	}
	var appErr *domain.AppError
	if !errors.As(err, &appErr) || appErr.Code != domain.CodeInvalidInput {
		t.Errorf("error = %v, want an AppError with code %q", err, domain.CodeInvalidInput)
	}
	if out.State != "" {
		t.Errorf("state = %q on a rejected call, want empty", out.State)
	}
	if sender.calls != 0 {
		t.Errorf("sender calls = %d, want 0 for an unknown receipt state", sender.calls)
	}
}

// TestWakeReportsCallerState proves every state reaches the receipt and that
// states which do not reach the recipient force OK false. The ADR 0026
// delivery value is left untouched in every case.
func TestWakeReportsCallerState(t *testing.T) {
	cases := []struct {
		state  DeliveryState
		wantOK bool
	}{
		{StateSpooled, false},
		{StatePending, false},
		{StateHeld, false},
		{StatePushed, true},
		{StatePushUnreachable, false},
		{StateRead, true},
		{StateRefused, false},
		{StateExpired, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			out, err := NewWakeHandler(&fakeWakeSender{delivery: DeliverySteer}).Wake(
				context.Background(), WakeInput{Target: "s", Text: "p", State: tc.state})
			if err != nil {
				t.Fatalf("Wake() error = %v", err)
			}
			if out.State != tc.state {
				t.Errorf("state = %q, want %q", out.State, tc.state)
			}
			if out.OK != tc.wantOK {
				t.Errorf("ok = %v, want %v for state %q", out.OK, tc.wantOK, tc.state)
			}
			if out.Delivery != DeliverySteer {
				t.Errorf("delivery = %q, want %q; the receipt state must not rewrite delivery", out.Delivery, DeliverySteer)
			}
		})
	}
}

// TestWakeRefusedIsNotRead is the ADR 0043 acceptance guard: a refused message
// is never reported as read or as a delivered follow-up, even when a channel
// acknowledged the send.
func TestWakeRefusedIsNotRead(t *testing.T) {
	for _, state := range []DeliveryState{StateRefused, StateExpired, StatePushUnreachable} {
		if state.IsRead() {
			t.Errorf("state %q reports IsRead=true, want false", state)
		}
	}
	if !StateRead.IsRead() {
		t.Error("StateRead reports IsRead=false, want true")
	}

	out, err := NewWakeHandler(&fakeWakeSender{delivery: DeliveryQueue}).Wake(
		context.Background(), WakeInput{Target: "s", Text: "p", State: StateRefused})
	if err != nil {
		t.Fatalf("Wake() error = %v", err)
	}
	if out.State != StateRefused || out.State.IsRead() {
		t.Errorf("state=%q IsRead=%v, want refused/false", out.State, out.State.IsRead())
	}
	if out.OK {
		t.Error("ok = true for a refused message, want false")
	}
	if !strings.Contains(out.Reason, "refused") {
		t.Errorf("reason = %q, want it to explain the refused receipt", out.Reason)
	}
}

// TestWakeDefaultsReceiptStateFromDelivery keeps older callers working: with
// no State the core claims only what the delivery outcome proves, so a
// delivered follow-up is pushed and every refusal or interruption is held.
func TestWakeDefaultsReceiptStateFromDelivery(t *testing.T) {
	cases := []struct {
		name   string
		in     WakeInput
		sender Delivery
		want   DeliveryState
		wantOK bool
	}{
		{name: "steer", in: WakeInput{Target: "s", Text: "p"}, sender: DeliverySteer, want: StatePushed, wantOK: true},
		{name: "queue", in: WakeInput{Target: "s", Text: "p"}, sender: DeliveryQueue, want: StatePushed, wantOK: true},
		{name: "unsupported", in: WakeInput{Target: "s", Text: "p"}, sender: DeliveryUnsupported, want: StateHeld},
		{name: "interrupted", in: WakeInput{Target: "s", Text: "p", HasPendingQuestion: true}, sender: DeliverySteer, want: StateHeld},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := NewWakeHandler(&fakeWakeSender{delivery: tc.sender}).Wake(context.Background(), tc.in)
			if err != nil {
				t.Fatalf("Wake() error = %v", err)
			}
			if out.State != tc.want {
				t.Errorf("state = %q, want %q", out.State, tc.want)
			}
			if out.OK != tc.wantOK {
				t.Errorf("ok = %v, want %v", out.OK, tc.wantOK)
			}
		})
	}
}

// TestWakeFallbackTraceDefaultsToSpooled covers the fallback path: a message
// written to the task trace for a later retry is spooled, not delivered.
func TestWakeFallbackTraceDefaultsToSpooled(t *testing.T) {
	sender := &fakeFallbackSender{results: map[WakeChannel]channelResult{
		ChannelSteer: {delivery: DeliverySteer},
		ChannelQueue: {delivery: DeliveryQueue},
	}}
	out, err := NewFallbackWakeHandler(sender, &fakeConfirmer{}, &fakeTracer{}).WakeWithFallback(
		context.Background(), WakeInput{Target: "s", Text: "p"})
	if err != nil {
		t.Fatalf("WakeWithFallback() error = %v", err)
	}
	if out.State != StateSpooled || !out.Traced || out.OK {
		t.Errorf("state=%q traced=%v ok=%v, want spooled/true/false", out.State, out.Traced, out.OK)
	}
}

// TestWakeStateOverInMemoryTransport drives the receipt state through a real
// MCP client and server. It proves the state is decoded from the schema and
// echoed on the receipt, not just handled in process.
func TestWakeStateOverInMemoryTransport(t *testing.T) {
	server := NewServer()
	AttachWake(server, NewWakeHandler(&fakeWakeSender{delivery: DeliveryQueue}))
	session := connectWakeServer(t, server)

	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: WakeToolName,
		Arguments: WakeInput{
			Target: "session-1", Text: "obligation-1@7", State: StateRead,
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
	if out.State != StateRead || !out.State.IsRead() || !out.OK {
		t.Errorf("client saw state=%q IsRead=%v ok=%v, want read/true/true", out.State, out.State.IsRead(), out.OK)
	}
}

// TestWakeSchemaHasState proves an MCP client can send the receipt state, so
// the environment can report where in its life a message actually is.
func TestWakeSchemaHasState(t *testing.T) {
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
	if !strings.Contains(string(inputSchema), "state") {
		t.Errorf("wake input schema missing state: %s", inputSchema)
	}
}

// TestT3_110WakeValidationErrorIsRepairable proves a rejected wake call carries
// the repairable envelope: the six ADR 0009 fields plus retryable, recoveryHint
// and partial.cursor (ADR 0046 feature 2). The error still unwraps to AppError.
func TestT3_110WakeValidationErrorIsRepairable(t *testing.T) {
	cases := []struct {
		name       string
		in         WakeInput
		wantCursor string
	}{
		{name: "text", in: WakeInput{Target: "s"}, wantCursor: recoveryCursorText},
		{name: "source", in: WakeInput{Target: "s", Text: "p", Source: "bogus"}, wantCursor: recoveryCursorSource},
		{name: "state", in: WakeInput{Target: "s", Text: "p", State: "bogus"}, wantCursor: recoveryCursorState},
		{name: "target", in: WakeInput{Text: "p"}, wantCursor: recoveryCursorTarget},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewWakeHandler(&fakeWakeSender{delivery: DeliverySteer})
			_, err := handler.Wake(context.Background(), tc.in)
			if err == nil {
				t.Fatal("Wake() error = nil, want a repairable error")
			}
			var appErr *domain.AppError
			if !errors.As(err, &appErr) || appErr.Code != domain.CodeInvalidInput {
				t.Fatalf("error %v does not unwrap to AppError code %q", err, domain.CodeInvalidInput)
			}
			shape, ok := ToRepairableErrorShape(err)
			if !ok {
				t.Fatal("ToRepairableErrorShape returned false")
			}
			if shape.OK || shape.Code != domain.CodeInvalidInput || shape.Status != 400 {
				t.Errorf("shape ok=%v code=%q status=%d, want false/%q/400", shape.OK, shape.Code, shape.Status, domain.CodeInvalidInput)
			}
			if !shape.Retryable {
				t.Error("retryable=false, want true for a fixable input error")
			}
			if shape.RecoveryHint == "" {
				t.Error("recoveryHint is empty")
			}
			if shape.Partial.Cursor != tc.wantCursor {
				t.Errorf("partial.cursor=%q, want %q", shape.Partial.Cursor, tc.wantCursor)
			}
		})
	}
}

// TestT3_110RecoveryHintLetsModelFixOneParameterAndContinue is the acceptance
// check: the model reads recoveryHint and partial.cursor, fixes the single named
// parameter, and the retried call reaches the sender instead of resending all.
func TestT3_110RecoveryHintLetsModelFixOneParameterAndContinue(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	handler := NewWakeHandler(sender)

	bad := WakeInput{Target: "session-1", Text: "obligation-1@7", Source: "revie"}
	_, err := handler.Wake(context.Background(), bad)
	if err == nil {
		t.Fatal("Wake() error = nil, want a repairable error for a misspelled source")
	}
	shape, ok := ToRepairableErrorShape(err)
	if !ok {
		t.Fatal("ToRepairableErrorShape returned false")
	}
	if shape.Partial.Cursor != recoveryCursorSource {
		t.Fatalf("partial.cursor=%q, want %q", shape.Partial.Cursor, recoveryCursorSource)
	}
	if !strings.Contains(shape.RecoveryHint, "source") {
		t.Errorf("recoveryHint %q does not name the parameter to fix", shape.RecoveryHint)
	}
	if sender.calls != 0 {
		t.Fatalf("sender calls=%d, want 0 before the parameter is fixed", sender.calls)
	}

	fixed := bad
	fixed.Source = SourceReview
	out, err := handler.Wake(context.Background(), fixed)
	if err != nil {
		t.Fatalf("Wake() after fix error = %v, want nil", err)
	}
	if !out.OK || out.Source != SourceReview {
		t.Errorf("after fix ok=%v source=%q, want true/%q", out.OK, out.Source, SourceReview)
	}
	if sender.calls != 1 {
		t.Errorf("sender calls=%d, want exactly one delivery after the fix", sender.calls)
	}
}

// TestT3_110RepairableShapeKeepsADR0009Form proves the six original fields are
// preserved, including code-specific retryAfterMs and newWatchID, and that the
// JSON envelope carries exactly the three added recovery fields on top.
func TestT3_110RepairableShapeKeepsADR0009Form(t *testing.T) {
	base := domain.NewAppError(domain.CodeRateLimited, "slow down")
	ms := int64(250)
	base.RetryAfterMs = &ms
	base.NewWatchID = "watch-1"
	err := repairableError(base, true, "wake: set the source field to manual or review", recoveryCursorSource)

	six, ok := ToErrorShape(err)
	if !ok {
		t.Fatal("ToErrorShape no longer recognises a RecoveryError as an AppError")
	}
	if six.OK || six.Code != domain.CodeRateLimited || six.Status != 429 || six.Message != "slow down" {
		t.Errorf("ADR 0009 shape changed: %+v", six)
	}
	if six.RetryAfterMs == nil || *six.RetryAfterMs != ms || six.NewWatchID != "watch-1" {
		t.Errorf("code-specific fields lost: retryAfterMs=%v newWatchID=%q", six.RetryAfterMs, six.NewWatchID)
	}

	shape, ok := ToRepairableErrorShape(err)
	if !ok {
		t.Fatal("ToRepairableErrorShape returned false")
	}
	if shape.RetryAfterMs == nil || *shape.RetryAfterMs != ms || shape.NewWatchID != "watch-1" {
		t.Errorf("repairable shape lost code-specific fields: %+v", shape.ErrorShape)
	}

	raw, err := json.Marshal(shape)
	if err != nil {
		t.Fatalf("marshal repairable shape: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal repairable shape: %v", err)
	}
	for _, key := range []string{"ok", "code", "message", "status", "retryable", "recoveryHint", "partial"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("repairable shape JSON missing %q: %s", key, raw)
		}
	}
	partial, ok := fields["partial"].(map[string]any)
	if !ok || partial["cursor"] != recoveryCursorSource {
		t.Errorf("partial.cursor lost in JSON: %s", raw)
	}
}

// TestT3_110CancelledWaitIsRepairable keeps a cancelled stagger repairable too,
// pointing the model at the wake window instead of a dead session.
func TestT3_110CancelledWaitIsRepairable(t *testing.T) {
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
		t.Fatalf("error %v does not unwrap to AppError code %q", err, domain.CodeCancelled)
	}
	shape, ok := ToRepairableErrorShape(err)
	if !ok {
		t.Fatal("ToRepairableErrorShape returned false")
	}
	if shape.Code != domain.CodeCancelled || !shape.Retryable {
		t.Errorf("shape code=%q retryable=%v, want %q/true", shape.Code, shape.Retryable, domain.CodeCancelled)
	}
	if shape.Partial.Cursor != recoveryCursorWindow {
		t.Errorf("partial.cursor=%q, want %q", shape.Partial.Cursor, recoveryCursorWindow)
	}
	if sender.calls != 0 {
		t.Errorf("sender calls=%d, want 0 after a cancelled wait", sender.calls)
	}
}

// TestT3_110NonAppErrorIsNotRepairable keeps the conversion honest: a plain
// error carries no contract code and must not fabricate a repairable envelope.
func TestT3_110NonAppErrorIsNotRepairable(t *testing.T) {
	if _, ok := ToRepairableErrorShape(errors.New("plain failure")); ok {
		t.Error("ToRepairableErrorShape accepted an error with no AppError")
	}
}

// ---------------------------------------------------------------------------
// T3.121 / ADR 0051 p5: transactional confirmation of token and deadline.
// ---------------------------------------------------------------------------

// confirmationClock is a controllable clock for the deadline checks. Its lock
// lets the concurrency test share it safely with the ledger.
type confirmationClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *confirmationClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *confirmationClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestConfirmationLedger() (*ConfirmationLedger, *confirmationClock) {
	ledger := NewConfirmationLedger()
	clock := &confirmationClock{t: time.Unix(1_700_000_000, 0)}
	ledger.now = clock.Now
	return ledger, clock
}

// TestT3_121AckVerifiesTokenStateAndDeadline is the acceptance core: Ack
// rejects a stale token, then verifies the pending state and the unexpired
// deadline in one transaction before recording the ack.
func TestT3_121AckVerifiesTokenStateAndDeadline(t *testing.T) {
	ledger, clock := newTestConfirmationLedger()
	token, err := ledger.Issue("task-1", time.Minute)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if token == "" {
		t.Fatal("Issue() returned an empty token")
	}

	stale := ledger.Ack("task-1", "old-worker-token")
	if !IsConfirmationRejected(stale) {
		t.Fatalf("Ack(stale token) error = %v, want a confirmation rejection", stale)
	}
	var rejection *ConfirmationError
	if !errors.As(stale, &rejection) || rejection.Reason != confirmationReasonToken {
		t.Errorf("stale rejection = %v, want reason %q", stale, confirmationReasonToken)
	}
	if state, _ := ledger.State("task-1"); state != ConfirmationPending {
		t.Errorf("state after stale token = %q, want %q", state, ConfirmationPending)
	}

	clock.Advance(30 * time.Second)
	if err := ledger.Ack("task-1", token); err != nil {
		t.Fatalf("Ack(current token, inside deadline) error = %v, want nil", err)
	}
	if state, _ := ledger.State("task-1"); state != ConfirmationAcked {
		t.Errorf("state after Ack = %q, want %q", state, ConfirmationAcked)
	}

	again := ledger.Ack("task-1", token)
	if !errors.As(again, &rejection) || rejection.Reason != confirmationReasonState {
		t.Errorf("second Ack = %v, want a non-pending rejection", again)
	}
	if state, _ := ledger.State("task-1"); state != ConfirmationAcked {
		t.Errorf("state after second Ack = %q, want %q unchanged", state, ConfirmationAcked)
	}

	if err := ledger.Ack("missing", token); !IsConfirmationRejected(err) {
		t.Errorf("Ack(unknown id) error = %v, want a rejection", err)
	}
}

// TestT3_121FailVerifiesTokenStateAndDeadline gives Fail the same three checks
// as Ack, so a stale worker cannot fail another worker's task either.
func TestT3_121FailVerifiesTokenStateAndDeadline(t *testing.T) {
	ledger, _ := newTestConfirmationLedger()
	token, err := ledger.Issue("task-1", time.Minute)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	if err := ledger.Fail("task-1", "stale"); !IsConfirmationRejected(err) {
		t.Errorf("Fail(stale token) error = %v, want a rejection", err)
	}
	if state, _ := ledger.State("task-1"); state != ConfirmationPending {
		t.Errorf("state after stale Fail = %q, want %q", state, ConfirmationPending)
	}

	if err := ledger.Fail("task-1", token); err != nil {
		t.Fatalf("Fail(current token) error = %v, want nil", err)
	}
	if state, _ := ledger.State("task-1"); state != ConfirmationFailed {
		t.Errorf("state after Fail = %q, want %q", state, ConfirmationFailed)
	}
	if err := ledger.Ack("task-1", token); !IsConfirmationRejected(err) {
		t.Errorf("Ack after Fail error = %v, want a rejection", err)
	}
}

// TestT3_121ExpiredDeadlineRejectedWithoutReaper proves an expired deadline is
// rejected and resolved by the confirmation itself. No goroutine runs to do it.
func TestT3_121ExpiredDeadlineRejectedWithoutReaper(t *testing.T) {
	ledger, clock := newTestConfirmationLedger()
	token, err := ledger.Issue("task-1", time.Minute)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	clock.Advance(2 * time.Minute)
	if state, _ := ledger.State("task-1"); state != ConfirmationPending {
		t.Errorf("state before any confirmation = %q, want %q; a background reaper must not run", state, ConfirmationPending)
	}

	expired := ledger.Ack("task-1", token)
	if !IsConfirmationExpired(expired) {
		t.Fatalf("Ack(past deadline) error = %v, want the deadline rejection", expired)
	}
	if state, _ := ledger.State("task-1"); state != ConfirmationExpired {
		t.Errorf("state after expired Ack = %q, want %q", state, ConfirmationExpired)
	}

	// Recovery reissues the id in place of a reaper sweep.
	reissued, err := ledger.Issue("task-1", time.Minute)
	if err != nil {
		t.Fatalf("Issue() after expiry error = %v, want nil", err)
	}
	if reissued == token {
		t.Error("reissued token equals the expired token, want a fresh random token")
	}
	if err := ledger.Ack("task-1", reissued); err != nil {
		t.Fatalf("Ack(reissued token) error = %v, want nil", err)
	}
}

// TestT3_121StaleTokenCannotConfirmAnotherTask is the ADR 0051 p5 guard: a
// lagging worker cannot confirm a task it does not own. Tokens are random and
// distinct, not the monotonic fencing tokens of T3.51.
func TestT3_121StaleTokenCannotConfirmAnotherTask(t *testing.T) {
	ledger, _ := newTestConfirmationLedger()
	tokenA, err := ledger.Issue("task-A", time.Minute)
	if err != nil {
		t.Fatalf("Issue(task-A) error = %v", err)
	}
	tokenB, err := ledger.Issue("task-B", time.Minute)
	if err != nil {
		t.Fatalf("Issue(task-B) error = %v", err)
	}
	if tokenA == tokenB {
		t.Fatal("two confirmations share a token, want independent random tokens")
	}

	if err := ledger.Ack("task-B", tokenA); !IsConfirmationRejected(err) {
		t.Errorf("Ack(task-B, tokenA) error = %v, want a rejection", err)
	}
	for _, id := range []string{"task-A", "task-B"} {
		if state, _ := ledger.State(id); state != ConfirmationPending {
			t.Errorf("state of %s = %q, want %q unchanged", id, state, ConfirmationPending)
		}
	}
}

// TestT3_121PendingConfirmationNotReissued keeps the token a caller holds
// authoritative until it is used, expired or failed.
func TestT3_121PendingConfirmationNotReissued(t *testing.T) {
	ledger, _ := newTestConfirmationLedger()
	token, err := ledger.Issue("task-1", time.Minute)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if _, err := ledger.Issue("task-1", time.Minute); !IsConfirmationRejected(err) {
		t.Errorf("second Issue() error = %v, want a rejection while pending", err)
	}
	if err := ledger.Ack("task-1", token); err != nil {
		t.Fatalf("Ack(original token) error = %v, want the original token to stay valid", err)
	}
}

// TestT3_121ConcurrentAckHasSingleWinner proves the write lock linearizes the
// transaction: many racers with the same token produce exactly one ack.
func TestT3_121ConcurrentAckHasSingleWinner(t *testing.T) {
	ledger, _ := newTestConfirmationLedger()
	token, err := ledger.Issue("task-1", time.Minute)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	const racers = 16
	var wg sync.WaitGroup
	results := make(chan error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- ledger.Ack("task-1", token)
		}()
	}
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Errorf("successful Acks = %d, want exactly 1 of %d", successes, racers)
	}
	if state, _ := ledger.State("task-1"); state != ConfirmationAcked {
		t.Errorf("state = %q, want %q", state, ConfirmationAcked)
	}
}

// TestT3_121WakeHandlerConfirmation wires the ledger into the wake handler. A
// handler without one refuses an honest confirmation instead of guessing.
func TestT3_121WakeHandlerConfirmation(t *testing.T) {
	ledger, _ := newTestConfirmationLedger()
	handler := NewWakeHandler(&fakeWakeSender{delivery: DeliverySteer}).WithConfirmations(ledger)
	token, err := ledger.Issue("wake-1", time.Minute)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	if err := handler.ConfirmDelivery("wake-1", token); err != nil {
		t.Fatalf("ConfirmDelivery() error = %v, want nil", err)
	}
	if state, _ := ledger.State("wake-1"); state != ConfirmationAcked {
		t.Errorf("state = %q, want %q", state, ConfirmationAcked)
	}
	if err := handler.FailDelivery("wake-1", token); !IsConfirmationRejected(err) {
		t.Errorf("FailDelivery() after ack error = %v, want a rejection", err)
	}

	without := NewWakeHandler(nil)
	if err := without.ConfirmDelivery("wake-1", token); !IsConfirmationRejected(err) {
		t.Errorf("ConfirmDelivery() without a ledger error = %v, want a rejection", err)
	}
}

// ---- ADR 0056 p3 / T3.124: active duplicate-prompt detection and operationId ----

// TestT3_124PromptGetsInstantOperationID proves the id exists at entry, before
// any delivery, and is echoed on a successful receipt.
func TestT3_124PromptGetsInstantOperationID(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	handler := NewWakeHandler(sender).WithOperationRegistry(NewMemoryOperationRegistry())

	out, err := handler.Wake(context.Background(), WakeInput{
		Target: "session-1",
		Text:   "obligation-1@7",
		Prompt: "review the PR and merge",
	})
	if err != nil {
		t.Fatalf("Wake() error = %v, want nil", err)
	}
	if out.OperationID == "" {
		t.Fatal("operationId is empty, want one issued at entry")
	}
	if !strings.HasPrefix(out.OperationID, operationIDPrefix) {
		t.Errorf("operationId = %q, want prefix %q", out.OperationID, operationIDPrefix)
	}
	if out.Duplicate {
		t.Error("duplicate = true on the first call, want false")
	}
	if !out.OK || out.Delivery != DeliverySteer {
		t.Errorf("receipt = delivery %q ok %v, want steer/true", out.Delivery, out.OK)
	}
	if sender.calls != 1 {
		t.Errorf("sender calls = %d, want 1", sender.calls)
	}
}

// TestT3_124EqualPromptReusesOperationIDAndStartsNoSecondTask is the acceptance
// test: a repeated prompt returns the existing id and starts no second task.
func TestT3_124EqualPromptReusesOperationIDAndStartsNoSecondTask(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	handler := NewWakeHandler(sender).WithOperationRegistry(NewMemoryOperationRegistry())
	in := WakeInput{Target: "session-1", Text: "obligation-1@7", Prompt: "review the PR and merge"}

	first, err := handler.Wake(context.Background(), in)
	if err != nil {
		t.Fatalf("first Wake() error = %v", err)
	}
	second, err := handler.Wake(context.Background(), in)
	if err != nil {
		t.Fatalf("second Wake() error = %v", err)
	}

	if first.OperationID == "" || second.OperationID != first.OperationID {
		t.Errorf("operationIds = %q then %q, want one non-empty id", first.OperationID, second.OperationID)
	}
	if first.Duplicate {
		t.Error("first duplicate = true, want false")
	}
	if !second.Duplicate {
		t.Error("second duplicate = false, want true")
	}
	if second.OK {
		t.Error("duplicate ok = true, want false: no second follow-up was delivered")
	}
	if second.Delivery != DeliveryUnsupported {
		t.Errorf("duplicate delivery = %q, want %q", second.Delivery, DeliveryUnsupported)
	}
	if second.Reason == "" {
		t.Error("duplicate reason is empty, want the reason it was refused")
	}
	if second.State != StateHeld {
		t.Errorf("duplicate state = %q, want %q", second.State, StateHeld)
	}
	if sender.calls != 1 {
		t.Errorf("sender calls = %d, want 1: a duplicate must not deliver again", sender.calls)
	}
}

// TestT3_124TrimsSurroundingWhitespace keeps a trailing newline from starting
// a second task while leaving the prompt content itself untouched.
func TestT3_124TrimsSurroundingWhitespace(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliveryQueue}
	handler := NewWakeHandler(sender).WithOperationRegistry(NewMemoryOperationRegistry())

	first, err := handler.Wake(context.Background(), WakeInput{Target: "s", Text: "p", Prompt: "  build and test\n"})
	if err != nil {
		t.Fatalf("first Wake() error = %v", err)
	}
	second, err := handler.Wake(context.Background(), WakeInput{Target: "s", Text: "p", Prompt: "build and test"})
	if err != nil {
		t.Fatalf("second Wake() error = %v", err)
	}

	if second.OperationID != first.OperationID || !second.Duplicate {
		t.Errorf("second = id %q duplicate %v, want the first id %q and duplicate true", second.OperationID, second.Duplicate, first.OperationID)
	}
	if sender.calls != 1 {
		t.Errorf("sender calls = %d, want 1", sender.calls)
	}
}

// TestT3_124DistinctPromptsStartDistinctTasks proves the index keys on prompt
// content, not on a single global operation.
func TestT3_124DistinctPromptsStartDistinctTasks(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	handler := NewWakeHandler(sender).WithOperationRegistry(NewMemoryOperationRegistry())

	first, err := handler.Wake(context.Background(), WakeInput{Target: "s", Text: "p1", Prompt: "build the API"})
	if err != nil {
		t.Fatalf("first Wake() error = %v", err)
	}
	second, err := handler.Wake(context.Background(), WakeInput{Target: "s", Text: "p2", Prompt: "review the docs"})
	if err != nil {
		t.Fatalf("second Wake() error = %v", err)
	}

	if first.OperationID == "" || second.OperationID == "" || first.OperationID == second.OperationID {
		t.Errorf("operationIds = %q and %q, want two distinct ids", first.OperationID, second.OperationID)
	}
	if first.Duplicate || second.Duplicate {
		t.Error("duplicate = true for a distinct prompt, want false")
	}
	if sender.calls != 2 {
		t.Errorf("sender calls = %d, want 2", sender.calls)
	}
}

// TestT3_124DifferentTargetsSamePromptIsOneTask is the line against the
// clientRequestId key of T2.22: dedup follows the prompt content even when the
// delivery target changes between calls.
func TestT3_124DifferentTargetsSamePromptIsOneTask(t *testing.T) {
	sender := &fakeWakeSender{delivery: DeliverySteer}
	handler := NewWakeHandler(sender).WithOperationRegistry(NewMemoryOperationRegistry())

	first, err := handler.Wake(context.Background(), WakeInput{Target: "session-A", Text: "p", Prompt: "same job"})
	if err != nil {
		t.Fatalf("first Wake() error = %v", err)
	}
	second, err := handler.Wake(context.Background(), WakeInput{Target: "session-B", Text: "p", Prompt: "same job"})
	if err != nil {
		t.Fatalf("second Wake() error = %v", err)
	}

	if second.OperationID != first.OperationID || !second.Duplicate {
		t.Errorf("second = id %q duplicate %v, want the first id %q and duplicate true", second.OperationID, second.Duplicate, first.OperationID)
	}
	if sender.calls != 1 {
		t.Errorf("sender calls = %d, want 1", sender.calls)
	}
}

// TestT3_124DuplicateSkipsFallbackChain proves a repeated prompt walks no
// channel of the ADR 0038 chain and writes no task trace.
func TestT3_124DuplicateSkipsFallbackChain(t *testing.T) {
	sender := &fakeFallbackSender{results: map[WakeChannel]channelResult{
		ChannelSteer: {delivery: DeliverySteer},
	}}
	confirmer := &fakeConfirmer{confirmed: map[WakeChannel]bool{ChannelSteer: true}}
	tracer := &fakeTracer{}
	handler := NewFallbackWakeHandler(sender, confirmer, tracer).WithOperationRegistry(NewMemoryOperationRegistry())
	in := WakeInput{Target: "s", Text: "p", Prompt: "long job"}

	first, err := handler.WakeWithFallback(context.Background(), in)
	if err != nil {
		t.Fatalf("first WakeWithFallback() error = %v", err)
	}
	second, err := handler.WakeWithFallback(context.Background(), in)
	if err != nil {
		t.Fatalf("second WakeWithFallback() error = %v", err)
	}

	if first.OperationID == "" || second.OperationID != first.OperationID || !second.Duplicate {
		t.Errorf("operationIds/duplicate = %q, %q, %v, want one id and a duplicate", first.OperationID, second.OperationID, second.Duplicate)
	}
	if len(sender.attempts) != 1 {
		t.Errorf("fallback attempts = %d, want 1", len(sender.attempts))
	}
	if tracer.calls != 0 {
		t.Errorf("trace calls = %d, want 0 for a duplicate", tracer.calls)
	}
}

// TestT3_124DuplicateDoesNotWait keeps the "instant" promise: a duplicate is
// refused at entry, before the ADR 0041 stagger sleeps.
func TestT3_124DuplicateDoesNotWait(t *testing.T) {
	sleeper := &recordingSleeper{}
	sender := &fakeWakeSender{delivery: DeliveryQueue}
	handler := NewWakeHandler(sender).
		WithOperationRegistry(NewMemoryOperationRegistry()).
		WithWakeSleeper(sleeper).
		WithWakePoint(func(time.Duration) time.Duration { return 0 })
	in := WakeInput{Target: "s", Text: "p", Prompt: "long job", WakeBetweenMillis: 100}

	if _, err := handler.Wake(context.Background(), in); err != nil {
		t.Fatalf("first Wake() error = %v", err)
	}
	if _, err := handler.Wake(context.Background(), in); err != nil {
		t.Fatalf("second Wake() error = %v", err)
	}
	if len(sleeper.points) != 1 {
		t.Errorf("sleeper calls = %d, want 1: a duplicate must not wait", len(sleeper.points))
	}
}

// TestT3_124LegacyCallersKeepTheirOutput keeps an empty prompt and a missing
// registry on the pre-feature path, as every earlier caller expects.
func TestT3_124LegacyCallersKeepTheirOutput(t *testing.T) {
	cases := []struct {
		name    string
		prompt  string
		withReg bool
	}{
		{name: "no prompt", withReg: true},
		{name: "no registry", prompt: "long job"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender := &fakeWakeSender{delivery: DeliverySteer}
			handler := NewWakeHandler(sender)
			if tc.withReg {
				handler = handler.WithOperationRegistry(NewMemoryOperationRegistry())
			}
			in := WakeInput{Target: "s", Text: "p", Prompt: tc.prompt}

			first, err := handler.Wake(context.Background(), in)
			if err != nil {
				t.Fatalf("first Wake() error = %v", err)
			}
			second, err := handler.Wake(context.Background(), in)
			if err != nil {
				t.Fatalf("second Wake() error = %v", err)
			}
			if first.OperationID != "" || second.OperationID != "" {
				t.Errorf("operationId = %q then %q, want empty", first.OperationID, second.OperationID)
			}
			if first.Duplicate || second.Duplicate {
				t.Error("duplicate = true on the legacy path, want false")
			}
			if sender.calls != 2 {
				t.Errorf("sender calls = %d, want 2", sender.calls)
			}
		})
	}
}

// TestT3_124ConcurrentEqualPromptsStartOneTask proves lookup and insert are
// atomic: one winner starts the task, every other caller gets its id.
func TestT3_124ConcurrentEqualPromptsStartOneTask(t *testing.T) {
	const callers = 32
	sender := &fakeWakeSender{delivery: DeliverySteer}
	handler := NewWakeHandler(sender).WithOperationRegistry(NewMemoryOperationRegistry())
	in := WakeInput{Target: "s", Text: "p", Prompt: "one job"}

	type answer struct {
		out WakeOutput
		err error
	}
	results := make(chan answer, callers)
	for i := 0; i < callers; i++ {
		go func() {
			out, err := handler.Wake(context.Background(), in)
			results <- answer{out: out, err: err}
		}()
	}

	winners, id := 0, ""
	for i := 0; i < callers; i++ {
		res := <-results
		if res.err != nil {
			t.Fatalf("Wake() error = %v", res.err)
		}
		if res.out.OperationID == "" {
			t.Fatal("operationId is empty")
		}
		if id == "" {
			id = res.out.OperationID
		}
		if res.out.OperationID != id {
			t.Errorf("operationId = %q, want %q", res.out.OperationID, id)
		}
		if !res.out.Duplicate {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("non-duplicate callers = %d, want 1", winners)
	}
	if sender.calls != 1 {
		t.Errorf("sender calls = %d, want 1", sender.calls)
	}
}

// TestT3_124WakeSchemaHasPrompt proves the prompt field reaches the input
// schema derived by go-sdk, so a real client can send it.
func TestT3_124WakeSchemaHasPrompt(t *testing.T) {
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
	if !strings.Contains(string(inputSchema), "prompt") {
		t.Errorf("wake input schema missing prompt: %s", inputSchema)
	}
}

// TestT3_124OverInMemoryTransport drives two equal prompts through a real MCP
// client and server session, so the schema and the output fields are checked
// on the same path CI exercises.
func TestT3_124OverInMemoryTransport(t *testing.T) {
	server := NewServer()
	sender := &fakeWakeSender{delivery: DeliveryQueue}
	AttachWake(server, NewWakeHandler(sender).WithOperationRegistry(NewMemoryOperationRegistry()))
	session := connectWakeServer(t, server)

	args := WakeInput{Target: "session-1", Text: "obligation-1@7", Prompt: "build and ship"}
	decode := func() WakeOutput {
		t.Helper()
		result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
			Name:      WakeToolName,
			Arguments: args,
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
		return out
	}

	first := decode()
	second := decode()
	if first.OperationID == "" || second.OperationID != first.OperationID {
		t.Errorf("operationIds = %q then %q, want one non-empty id", first.OperationID, second.OperationID)
	}
	if first.Duplicate || !second.Duplicate {
		t.Errorf("duplicate = %v then %v, want false then true", first.Duplicate, second.Duplicate)
	}
	if sender.calls != 1 {
		t.Errorf("sender calls = %d, want 1 over MCP", sender.calls)
	}
}

// ---------------------------------------------------------------------------
// T3.129 / ADR 0058 p2: addressed doorbell wake.
// ---------------------------------------------------------------------------

type listenResult struct {
	msg DoorbellMessage
	err error
}

func startDoorbellListener(ctx context.Context, d *Doorbell, address string) <-chan listenResult {
	ch := make(chan listenResult, 1)
	go func() {
		msg, err := d.Listen(ctx, address)
		ch <- listenResult{msg: msg, err: err}
	}()
	return ch
}

func awaitDoorbell(t *testing.T, ch <-chan listenResult) listenResult {
	t.Helper()
	select {
	case res := <-ch:
		return res
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a doorbell listener")
		return listenResult{}
	}
}

func assertDoorbellQuiet(t *testing.T, ch <-chan listenResult) {
	t.Helper()
	select {
	case res := <-ch:
		t.Fatalf("listener woke unexpectedly: msg=%+v err=%v", res.msg, res.err)
	default:
	}
}

// recordingDoorbell is a DoorbellPort that records rings and mirrors the
// addressed-only rule in its return value, so handler-level tests can assert
// the trigger fact without running a long-poll goroutine.
type recordingDoorbell struct {
	calls int
	last  DoorbellMessage
}

func (r *recordingDoorbell) Ring(msg DoorbellMessage) bool {
	r.calls++
	r.last = msg
	return !msg.Broadcast && msg.Address != ""
}

// TestT3_129AddressedWakeRingsListener is the acceptance test: a wake addressed
// to a mailbox rings that doorbell and releases the long-poll listener with the
// pointer-only message. Delivery is unchanged by the doorbell (ADR 0026).
func TestT3_129AddressedWakeRingsListener(t *testing.T) {
	d := NewDoorbell()
	sender := &fakeWakeSender{delivery: DeliverySteer}
	handler := NewWakeHandler(sender).WithDoorbell(d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := startDoorbellListener(ctx, d, "mailbox-A")

	out, err := handler.Wake(context.Background(), WakeInput{
		Target: "session-1", Text: "obligation-1@7", Mailbox: "mailbox-A",
	})
	if err != nil {
		t.Fatalf("Wake() error = %v", err)
	}
	if !out.DoorbellRang {
		t.Error("doorbellRang = false for an addressed mailbox, want true")
	}
	if out.Delivery != DeliverySteer || !out.OK {
		t.Errorf("receipt = delivery %q ok %v, want steer/true", out.Delivery, out.OK)
	}
	if sender.calls != 1 {
		t.Errorf("sender calls = %d, want 1: the doorbell is orthogonal to delivery", sender.calls)
	}

	res := awaitDoorbell(t, ch)
	if res.err != nil {
		t.Fatalf("listener error = %v, want nil", res.err)
	}
	if res.msg.Address != "mailbox-A" || res.msg.Text != "obligation-1@7" || res.msg.Broadcast {
		t.Errorf("message = %+v, want addressed mailbox-A with the pointer text", res.msg)
	}
}

// TestT3_129BroadcastDoesNotRingDoorbell proves the doorbell is addressed: a
// broadcast follow-up rings nothing and leaves the listener blocked, while a
// later addressed message to the same mailbox wakes it.
func TestT3_129BroadcastDoesNotRingDoorbell(t *testing.T) {
	d := NewDoorbell()
	sender := &fakeWakeSender{delivery: DeliveryQueue}
	handler := NewWakeHandler(sender).WithDoorbell(d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := startDoorbellListener(ctx, d, "mailbox-A")

	out, err := handler.Wake(context.Background(), WakeInput{
		Target: "session-1", Text: "obligation-1@7", Mailbox: "mailbox-A", Broadcast: true,
	})
	if err != nil {
		t.Fatalf("broadcast Wake() error = %v", err)
	}
	if out.DoorbellRang {
		t.Error("doorbellRang = true for a broadcast, want false")
	}
	assertDoorbellQuiet(t, ch)
	if sender.calls != 1 {
		t.Errorf("sender calls = %d, want 1: broadcast still delivers, it only skips the doorbell", sender.calls)
	}

	out, err = handler.Wake(context.Background(), WakeInput{
		Target: "session-1", Text: "obligation-2@8", Mailbox: "mailbox-A",
	})
	if err != nil {
		t.Fatalf("addressed Wake() error = %v", err)
	}
	if !out.DoorbellRang {
		t.Error("doorbellRang = false for the addressed wake, want true")
	}
	res := awaitDoorbell(t, ch)
	if res.err != nil {
		t.Fatalf("listener error = %v, want nil", res.err)
	}
	if res.msg.Text != "obligation-2@8" || res.msg.Broadcast {
		t.Errorf("message = %+v, want the addressed pointer text", res.msg)
	}
}

// TestT3_129OnlyAddressedMailboxWakes proves address isolation: a message for
// mailbox-B wakes only the listener on B, never the one on A.
func TestT3_129OnlyAddressedMailboxWakes(t *testing.T) {
	d := NewDoorbell()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := startDoorbellListener(ctx, d, "mailbox-A")
	b := startDoorbellListener(ctx, d, "mailbox-B")

	if !d.Ring(DoorbellMessage{Address: "mailbox-B", Text: "p"}) {
		t.Fatal("Ring(B) = false, want true")
	}
	resB := awaitDoorbell(t, b)
	if resB.err != nil || resB.msg.Address != "mailbox-B" {
		t.Errorf("listener B = %+v, want the message addressed to B", resB)
	}
	assertDoorbellQuiet(t, a)

	if !d.Ring(DoorbellMessage{Address: "mailbox-A", Text: "q"}) {
		t.Fatal("Ring(A) = false, want true")
	}
	resA := awaitDoorbell(t, a)
	if resA.err != nil || resA.msg.Address != "mailbox-A" {
		t.Errorf("listener A = %+v, want the message addressed to A", resA)
	}
}

// TestT3_129DoorbellRingRejectsBroadcastAndUnaddressed checks the boundary at
// the switchboard itself: only an addressed, non-broadcast message rings.
func TestT3_129DoorbellRingRejectsBroadcastAndUnaddressed(t *testing.T) {
	d := NewDoorbell()
	if d.Ring(DoorbellMessage{Address: "mailbox-A", Text: "p", Broadcast: true}) {
		t.Error("broadcast Ring = true, want false")
	}
	if d.Ring(DoorbellMessage{Text: "p"}) {
		t.Error("unaddressed Ring = true, want false")
	}
	if !d.Ring(DoorbellMessage{Address: "mailbox-A", Text: "p"}) {
		t.Error("addressed Ring = false, want true")
	}
}

// TestT3_129LongPollBlocksUntilCancel proves there is no timer trigger: a
// listener stays blocked with no message and returns only when its context is
// cancelled, which is the ADR 0017 ladder distinction for the doorbell.
func TestT3_129LongPollBlocksUntilCancel(t *testing.T) {
	d := NewDoorbell()
	ctx, cancel := context.WithCancel(context.Background())
	ch := startDoorbellListener(ctx, d, "mailbox-A")
	assertDoorbellQuiet(t, ch)
	cancel()
	res := awaitDoorbell(t, ch)
	if !errors.Is(res.err, context.Canceled) {
		t.Errorf("listener error = %v, want context.Canceled", res.err)
	}
}

// TestT3_129DuplicatePromptDoesNotRingDoorbell keeps T3.124 intact: a repeated
// prompt is refused at entry and rings no second time.
func TestT3_129DuplicatePromptDoesNotRingDoorbell(t *testing.T) {
	bell := &recordingDoorbell{}
	sender := &fakeWakeSender{delivery: DeliverySteer}
	handler := NewWakeHandler(sender).WithDoorbell(bell).WithOperationRegistry(NewMemoryOperationRegistry())
	in := WakeInput{Target: "session-1", Text: "p", Mailbox: "mailbox-A", Prompt: "long job"}

	first, err := handler.Wake(context.Background(), in)
	if err != nil {
		t.Fatalf("first Wake() error = %v", err)
	}
	if !first.DoorbellRang {
		t.Error("first doorbellRang = false, want true")
	}
	if bell.calls != 1 {
		t.Errorf("doorbell calls = %d after first wake, want 1", bell.calls)
	}

	second, err := handler.Wake(context.Background(), in)
	if err != nil {
		t.Fatalf("second Wake() error = %v", err)
	}
	if !second.Duplicate {
		t.Error("second duplicate = false, want true")
	}
	if second.DoorbellRang {
		t.Error("duplicate doorbellRang = true, want false: no second ring")
	}
	if bell.calls != 1 {
		t.Errorf("doorbell calls = %d after duplicate, want 1", bell.calls)
	}
	if bell.last.Address != "mailbox-A" || bell.last.Text != "p" || bell.last.Broadcast {
		t.Errorf("ring = %+v, want the first addressed message", bell.last)
	}
}

// TestT3_129BroadcastPassesThroughHandler proves the handler forwards the
// broadcast flag to the port and reports the honest false trigger fact.
func TestT3_129BroadcastPassesThroughHandler(t *testing.T) {
	bell := &recordingDoorbell{}
	handler := NewWakeHandler(&fakeWakeSender{delivery: DeliveryQueue}).WithDoorbell(bell)

	out, err := handler.Wake(context.Background(), WakeInput{
		Target: "s", Text: "p", Mailbox: "mailbox-A", Broadcast: true,
	})
	if err != nil {
		t.Fatalf("Wake() error = %v", err)
	}
	if out.DoorbellRang {
		t.Error("doorbellRang = true for a broadcast, want false")
	}
	if bell.calls != 1 || !bell.last.Broadcast || bell.last.Address != "mailbox-A" {
		t.Errorf("ring = calls %d %+v, want one broadcast ring to mailbox-A", bell.calls, bell.last)
	}
}

// TestT3_129FallbackWakeRingsDoorbell proves the fallback path carries the same
// addressed trigger fact and keeps its honest delivery receipt.
func TestT3_129FallbackWakeRingsDoorbell(t *testing.T) {
	bell := &recordingDoorbell{}
	sender := &fakeFallbackSender{results: map[WakeChannel]channelResult{
		ChannelSteer: {delivery: DeliverySteer},
	}}
	confirmer := &fakeConfirmer{confirmed: map[WakeChannel]bool{ChannelSteer: true}}
	handler := NewFallbackWakeHandler(sender, confirmer, &fakeTracer{}).WithDoorbell(bell)

	out, err := handler.WakeWithFallback(context.Background(), WakeInput{
		Target: "s", Text: "p", Mailbox: "mailbox-A",
	})
	if err != nil {
		t.Fatalf("WakeWithFallback() error = %v", err)
	}
	if !out.DoorbellRang {
		t.Error("doorbellRang = false on the fallback path, want true")
	}
	if out.Delivery != DeliverySteer || !out.OK {
		t.Errorf("receipt = delivery %q ok %v, want steer/true", out.Delivery, out.OK)
	}
	if bell.calls != 1 {
		t.Errorf("doorbell calls = %d, want 1", bell.calls)
	}
}

// TestT3_129LegacyCallerWithoutDoorbell keeps an empty trigger fact when no
// port is attached, so callers written before ADR 0058 p2 are unchanged.
func TestT3_129LegacyCallerWithoutDoorbell(t *testing.T) {
	handler := NewWakeHandler(&fakeWakeSender{delivery: DeliverySteer})
	out, err := handler.Wake(context.Background(), WakeInput{
		Target: "s", Text: "p", Mailbox: "mailbox-A",
	})
	if err != nil {
		t.Fatalf("Wake() error = %v", err)
	}
	if out.DoorbellRang {
		t.Error("doorbellRang = true with no doorbell attached, want false")
	}
}

// TestT3_129ConcurrentRingsDoNotLoseMessages guards the switchboard's lock:
// concurrent rings to one address all reach a later listener.
func TestT3_129ConcurrentRingsDoNotLoseMessages(t *testing.T) {
	d := NewDoorbell()
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Ring(DoorbellMessage{Address: "mailbox-A", Text: "p"})
		}()
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		msg, err := d.Listen(ctx, "mailbox-A")
		cancel()
		if err != nil || msg.Address != "mailbox-A" {
			t.Fatalf("drain %d = %+v, %v; want an addressed message", i, msg, err)
		}
	}
}

// TestT3_129OverInMemoryTransport drives the mailbox through a real MCP client
// and server session, checking the schema and the structured output CI sees.
func TestT3_129OverInMemoryTransport(t *testing.T) {
	d := NewDoorbell()
	server := NewServer()
	AttachWake(server, NewWakeHandler(&fakeWakeSender{delivery: DeliveryQueue}).WithDoorbell(d))
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
	if !strings.Contains(string(inputSchema), "mailbox") {
		t.Errorf("wake input schema missing mailbox: %s", inputSchema)
	}
	if !strings.Contains(string(inputSchema), "broadcast") {
		t.Errorf("wake input schema missing broadcast: %s", inputSchema)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := startDoorbellListener(ctx, d, "mailbox-MCP")
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      WakeToolName,
		Arguments: WakeInput{Target: "session-1", Text: "obligation-1@7", Mailbox: "mailbox-MCP"},
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
	if !out.DoorbellRang {
		t.Error("doorbellRang missing from structured output over MCP, want true")
	}
	res := awaitDoorbell(t, ch)
	if res.err != nil || res.msg.Address != "mailbox-MCP" {
		t.Errorf("listener = %+v, want the message addressed to mailbox-MCP", res)
	}
}

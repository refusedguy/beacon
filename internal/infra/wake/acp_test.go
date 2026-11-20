package wake

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

type fakeACPTransport struct {
	requests []ACPRequest
	response []byte
	err      error
}

func (f *fakeACPTransport) Call(_ context.Context, req ACPRequest) ([]byte, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.response != nil {
		return f.response, nil
	}
	return []byte(`{"jsonrpc":"2.0","id":1,"result":{"messageId":"msg_1"}}`), nil
}

func TestACPAdapterDeliverSendsPromptEnvelope(t *testing.T) {
	fake := &fakeACPTransport{}
	adapter := &ACPAdapter{Transport: fake}
	p := domain.WakePayload{
		WatchID:      "watch_7",
		ObligationID: "obl_7",
		Revision:     3,
		Verdict:      "retry",
	}

	if err := adapter.Deliver(context.Background(), "sess_abc123", p); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("transport calls = %d, want 1", len(fake.requests))
	}
	req := fake.requests[0]
	if req.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", req.JSONRPC)
	}
	if req.ID <= 0 {
		t.Errorf("id = %d, want a positive request id", req.ID)
	}
	if req.Method != "session/prompt" {
		t.Errorf("method = %q, want session/prompt", req.Method)
	}
	params, ok := req.Params.(ACPPromptParams)
	if !ok {
		t.Fatalf("params type = %T, want ACPPromptParams", req.Params)
	}
	if params.SessionID != "sess_abc123" {
		t.Errorf("sessionId = %q, want sess_abc123", params.SessionID)
	}
	if len(params.Prompt) != 1 {
		t.Fatalf("prompt blocks = %d, want 1", len(params.Prompt))
	}
	block := params.Prompt[0]
	if block.Type != "text" {
		t.Errorf("block type = %q, want text", block.Type)
	}
	if want := Envelope(p); block.Text != want {
		t.Errorf("prompt text = %q, want %q", block.Text, want)
	}
	if strings.Contains(block.Text, "ignore previous instructions") {
		t.Errorf("prompt text carries arbitrary instructions: %q", block.Text)
	}
}

func TestACPEnvelopeWhitelistsPointerFields(t *testing.T) {
	p := domain.WakePayload{
		WatchID:      "watch_7",
		ObligationID: "obl_7",
		Revision:     3,
		Verdict:      "retry",
	}
	text := Envelope(p)
	if !strings.HasPrefix(text, "<beacon-event>") || !strings.HasSuffix(text, "</beacon-event>") {
		t.Fatalf("bad envelope: %q", text)
	}
	for _, want := range []string{
		`"watchID":"watch_7"`,
		`"obligationID":"obl_7"`,
		`"revision":3`,
		`"verdict":"retry"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("envelope missing %q: %s", want, text)
		}
	}
	for _, extra := range []string{"checkBody", "prTitle", "title", "body", "message"} {
		if strings.Contains(text, extra) {
			t.Errorf("envelope carries non-whitelisted field %q: %s", extra, text)
		}
	}
}

func TestACPAdapterRejectsEmptySessionID(t *testing.T) {
	fake := &fakeACPTransport{}
	adapter := &ACPAdapter{Transport: fake}
	if err := adapter.Deliver(context.Background(), "", domain.WakePayload{WatchID: "w"}); err == nil {
		t.Fatal("Deliver with empty session id succeeded")
	}
	if len(fake.requests) != 0 {
		t.Errorf("transport calls = %d, want 0", len(fake.requests))
	}
}

func TestACPAdapterReportsRPCError(t *testing.T) {
	fake := &fakeACPTransport{
		response: []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"message":"Unknown session"}}`),
	}
	adapter := &ACPAdapter{Transport: fake}
	err := adapter.Deliver(context.Background(), "sess_abc123", domain.WakePayload{WatchID: "w"})
	if err == nil || !strings.Contains(err.Error(), "Unknown session") {
		t.Fatalf("Deliver error = %v, want the JSON-RPC error message", err)
	}
}

func TestACPAdapterWrapsTransportError(t *testing.T) {
	fake := &fakeACPTransport{err: context.DeadlineExceeded}
	adapter := &ACPAdapter{Transport: fake}
	err := adapter.Deliver(context.Background(), "sess_abc123", domain.WakePayload{WatchID: "w"})
	if err == nil {
		t.Fatal("Deliver succeeded after a transport error")
	}
}

func TestACPHTTPTransportPostsJSONRPC(t *testing.T) {
	var got ACPRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"messageId":"msg_2"}}`)
	}))
	defer server.Close()

	transport := &ACPHTTPTransport{URL: server.URL}
	adapter := &ACPAdapter{Transport: transport}
	p := domain.WakePayload{WatchID: "watch_9", ObligationID: "obl_9", Revision: 4, Verdict: "ready"}
	if err := adapter.Deliver(context.Background(), "sess_http", p); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got.Method != "session/prompt" {
		t.Errorf("method = %q, want session/prompt", got.Method)
	}
	var params ACPPromptParams
	raw, err := json.Marshal(got.Params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if params.SessionID != "sess_http" {
		t.Errorf("sessionId = %q, want sess_http", params.SessionID)
	}
	if len(params.Prompt) != 1 || params.Prompt[0].Text != Envelope(p) {
		t.Errorf("prompt = %+v, want a single block with the envelope", params.Prompt)
	}
}

func TestACPStdioTransportFramesRequest(t *testing.T) {
	var framed []byte
	transport := &ACPStdioTransport{
		Exchange: func(_ context.Context, request []byte) ([]byte, error) {
			framed = append([]byte(nil), request...)
			return []byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"messageId\":\"msg_3\"}}\n"), nil
		},
	}
	adapter := &ACPAdapter{Transport: transport}
	if err := adapter.Deliver(context.Background(), "sess_stdio", domain.WakePayload{WatchID: "w"}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(framed) == 0 || framed[len(framed)-1] != '\n' {
		t.Fatalf("stdio frame = %q, want a newline-terminated JSON-RPC request", framed)
	}
	var req ACPRequest
	if err := json.Unmarshal(framed, &req); err != nil {
		t.Fatalf("decode stdio frame: %v", err)
	}
	if req.Method != "session/prompt" {
		t.Errorf("method = %q, want session/prompt", req.Method)
	}
}

// ===== ACP capability negotiation (S3.005) =====

func TestParseACPCapabilities(t *testing.T) {
	full := parseACPCapabilities(json.RawMessage(`{"agentCapabilities":{"loadSession":true,"fork":true,"models":true,"modes":true,"mcp":true}}`))
	if !full.SessionLoad || !full.Fork || !full.Models || !full.Modes || !full.MCPTransport {
		t.Fatalf("capabilities = %+v, want all five flags set", full)
	}
	if !full.Has(ACPCapSessionLoad) || full.Has(ACPCapability("unknown")) {
		t.Fatalf("Has() disagrees with capabilities %+v", full)
	}

	// An agent that declares nothing must not be treated as capable.
	none := parseACPCapabilities(json.RawMessage(`{"agentCapabilities":{}}`))
	if none != (ACPCapabilities{}) {
		t.Fatalf("capabilities = %+v, want the zero value", none)
	}

	// A malformed, missing or partial capability block degrades to zero rather
	// than failing the handshake.
	for _, raw := range []string{
		`not json`,
		`{}`,
		`{"agentCapabilities":null}`,
		`{"agentCapabilities":{"fork":false,"models":42,"mcp":"yes"}}`,
	} {
		if got := parseACPCapabilities(json.RawMessage(raw)); got != (ACPCapabilities{}) {
			t.Errorf("parseACPCapabilities(%s) = %+v, want the zero value", raw, got)
		}
	}

	// Some harnesses name the block serverCapabilities instead.
	if got := parseACPCapabilities(json.RawMessage(`{"serverCapabilities":{"loadSession":true}}`)); !got.SessionLoad {
		t.Errorf("serverCapabilities alias ignored: %+v", got)
	}
}

func TestBeaconClientCapabilitiesNotEmpty(t *testing.T) {
	caps := beaconClientCapabilities()
	if len(caps) == 0 {
		t.Fatal("initialize would send an empty clientCapabilities object")
	}
	if _, ok := caps[string(ACPCapSessionLoad)]; !ok {
		t.Errorf("clientCapabilities = %v, want a session/load declaration", caps)
	}
}

func TestACPDeliveryPolicyDegradesWithoutCapabilities(t *testing.T) {
	full := ACPCapabilities{SessionLoad: true, Fork: true, Models: true, Modes: true, MCPTransport: true}
	plan := (ACPDeliveryPolicy{PreferResume: true}).Plan(full)
	if plan.Mode != ACPDeliveryResume {
		t.Errorf("mode = %q, want resume", plan.Mode)
	}
	if !plan.UseFork || !plan.SendModels || !plan.SendModes || !plan.SendMCP {
		t.Errorf("plan = %+v, want every advertised optional feature enabled", plan)
	}

	// The acceptance rule: no capabilities still produces a usable plan, only
	// the mode changes.
	degraded := (ACPDeliveryPolicy{PreferResume: true}).Plan(ACPCapabilities{})
	if degraded.Mode != ACPDeliveryNewRun {
		t.Errorf("mode = %q, want newRun (degraded)", degraded.Mode)
	}
	if degraded.UseFork || degraded.SendModels || degraded.SendModes || degraded.SendMCP {
		t.Errorf("plan = %+v, want every optional feature dropped", degraded)
	}

	// Without a resume preference a capable agent still starts a new run.
	newRun := (ACPDeliveryPolicy{}).Plan(full)
	if newRun.Mode != ACPDeliveryNewRun {
		t.Errorf("mode = %q, want newRun", newRun.Mode)
	}
}

func TestPersistentACPInitializeNegotiatesCapabilities(t *testing.T) {
	client, _ := newW2BACPPipe(false)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !client.Capabilities().SessionLoad {
		t.Fatalf("negotiated capabilities = %+v, want session/load", client.Capabilities())
	}
	plan := client.DeliveryPlan(ACPDeliveryPolicy{PreferResume: true})
	if plan.Mode != ACPDeliveryResume {
		t.Errorf("delivery plan = %+v, want resume", plan)
	}
}

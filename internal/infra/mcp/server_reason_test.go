package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/pkg/core"
)

// reasonClient connects a fresh server to an in-memory client. It keeps its own
// setup so this file does not depend on the server_test.go singleton.
func reasonClient(t *testing.T, sv Services) *msdk.ClientSession {
	t.Helper()
	server := NewServer(sv)
	clientTransport, serverTransport := msdk.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := msdk.NewClient(&msdk.Implementation{Name: "reason-test-client"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func reasonControl(t *testing.T, cs *msdk.ClientSession, args map[string]any) *msdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name:      "beacon_control",
		Arguments: args,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func reasonText(t *testing.T, res *msdk.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatalf("empty tool result: %+v", res)
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatalf("unexpected content: %+v", res.Content)
	}
	return text.Text
}

func reasonRefusal(t *testing.T, text string) core.WireControlResult {
	t.Helper()
	var refusal core.WireControlResult
	if err := json.Unmarshal([]byte(text), &refusal); err != nil {
		t.Fatalf("decode refusal %q: %v", text, err)
	}
	return refusal
}

// TestServerPreservesControlRefusal checks the whole server boundary. The
// remote adapter reports a structured 409 and the caller must receive a wire
// refusal with every producer field intact.
func TestServerPreservesControlRefusal(t *testing.T) {
	sv := Services{Remote: func(_ context.Context, _, _ string, _ int, _ string) error {
		return errors.New("beacon API status 409: run is active")
	}}
	cs := reasonClient(t, sv)

	res := reasonControl(t, cs, map[string]any{"action": "unblock", "run_id": "r1"})
	if !res.IsError {
		t.Fatal("refused control reported success")
	}
	refusal := reasonRefusal(t, reasonText(t, res))
	if refusal.OK || refusal.Applied {
		t.Fatalf("refusal = %+v, want ok=false applied=false", refusal)
	}
	if refusal.Action != "unblock" || refusal.RunID != "r1" {
		t.Fatalf("refusal = %+v, want action unblock run r1", refusal)
	}
	if refusal.Reason != "run is active" {
		t.Fatalf("reason = %q, want %q", refusal.Reason, "run is active")
	}
}

// TestServerKeepsBareStatusPlain pins the refusal detector. A status line with
// no reason must not turn into a refusal with an invented reason.
func TestServerKeepsBareStatusPlain(t *testing.T) {
	sv := Services{Remote: func(_ context.Context, _, _ string, _ int, _ string) error {
		return errors.New("beacon API status 409")
	}}
	cs := reasonClient(t, sv)

	res := reasonControl(t, cs, map[string]any{"action": "unblock", "run_id": "r1"})
	if !res.IsError || !strings.Contains(reasonText(t, res), "409") {
		t.Fatalf("result = %+v, text = %q", res, reasonText(t, res))
	}
}

// TestServerKeepsUnrelatedControlErrorPlain checks that the boundary only maps
// a daemon refusal. Other errors keep the SDK's text so callers can still see
// the underlying failure.
func TestServerKeepsUnrelatedControlErrorPlain(t *testing.T) {
	sv := Services{Remote: func(_ context.Context, _, _ string, _ int, _ string) error {
		return errors.New("no live beacon daemon")
	}}
	cs := reasonClient(t, sv)

	res := reasonControl(t, cs, map[string]any{"action": "cancel", "run_id": "r1"})
	if !res.IsError || !strings.Contains(reasonText(t, res), "no live beacon daemon") {
		t.Fatalf("result = %+v, text = %q", res, reasonText(t, res))
	}
}

// TestServerControlSuccessStillReturnsOK guards the applied path. A successful
// control call must not pick up refusal metadata from the middleware.
func TestServerControlSuccessStillReturnsOK(t *testing.T) {
	sv := Services{Remote: func(_ context.Context, _, _ string, _ int, _ string) error {
		return nil
	}}
	cs := reasonClient(t, sv)

	res := reasonControl(t, cs, map[string]any{"action": "cancel", "run_id": "r1"})
	if res.IsError || reasonText(t, res) != "ok" {
		t.Fatalf("result = %+v, text = %q", res, reasonText(t, res))
	}
}

// TestControlRefusalResultRequiresReason exercises the mapper directly, so a
// decision to stop using it at the boundary would still fail a test.
func TestControlRefusalResultRequiresReason(t *testing.T) {
	if _, ok := controlRefusalResult("cancel", "r1", nil); ok {
		t.Fatal("nil error mapped to a refusal")
	}
	for _, msg := range []string{"beacon API status 409", "beacon API status 409:   ", " daemon busy"} {
		if _, ok := controlRefusalResult("cancel", "r1", errors.New(msg)); ok {
			t.Fatalf("error %q mapped to a refusal", msg)
		}
	}
	res, ok := controlRefusalResult("retry", "r2", errors.New("beacon API status 409: stale head"))
	if !ok {
		t.Fatal("structured refusal was not mapped")
	}
	if !res.IsError {
		t.Fatal("mapped refusal reported success")
	}
	refusal := reasonRefusal(t, reasonText(t, res))
	if refusal.Action != "retry" || refusal.RunID != "r2" || refusal.Reason != "stale head" {
		t.Fatalf("refusal = %+v", refusal)
	}
	if refusal.OK || refusal.Applied {
		t.Fatalf("refusal = %+v, want ok=false applied=false", refusal)
	}
}

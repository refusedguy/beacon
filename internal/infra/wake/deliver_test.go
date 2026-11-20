package wake

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

func TestEnvelopeWhitelistsFields(t *testing.T) {
	env := Envelope(domain.Pointer{WatchID: "watch-1", ObligationID: "obl-1", Revision: 7})
	if !strings.HasPrefix(env, "<beacon-event>") || !strings.HasSuffix(env, "</beacon-event>") {
		t.Fatalf("bad envelope: %q", env)
	}
	for _, want := range []string{`"watchID":"watch-1"`, `"obligationID":"obl-1"`, `"revision":7`} {
		if !strings.Contains(env, want) {
			t.Errorf("envelope missing %q: %s", want, env)
		}
	}
	if strings.Contains(env, "verdict") {
		t.Errorf("envelope must not carry verdict detail: %s", env)
	}
}

func TestPromptSyntaxesEquivalent(t *testing.T) {
	c := &CLI{}
	text := Envelope(domain.Pointer{WatchID: "w", ObligationID: "o", Revision: 1})

	rpc := c.PromptArgs(PromptRPC, "ses_test", text)
	path := c.PromptArgs(PromptPath, "ses_test", text)

	if rpc[1] != "session.prompt" || rpc[2] != "--param" || rpc[3] != "sessionID=ses_test" {
		t.Fatalf("rpc args = %q", rpc)
	}
	if path[1] != "POST" || path[2] != "/api/session/ses_test/prompt" {
		t.Fatalf("path args = %q", path)
	}
	if rpc[len(rpc)-2] != "--data" || path[len(path)-2] != "--data" {
		t.Fatalf("both syntaxes must carry --data: rpc=%q path=%q", rpc, path)
	}
	if rpc[len(rpc)-1] != path[len(path)-1] {
		t.Fatalf("--data differs:\nrpc  = %s\npath = %s", rpc[len(rpc)-1], path[len(path)-1])
	}

	var sent struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(rpc[len(rpc)-1]), &sent); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if sent.Delivery != "steer" || sent.Text != text {
		t.Fatalf("prompt body = %+v", sent)
	}
}

func TestServerFlagOnEveryCommand(t *testing.T) {
	c := &CLI{Server: "http://127.0.0.1:4096"}
	for name, args := range map[string][]string{
		"openapi": c.OpenAPIArgs(),
		"rpc":     c.PromptArgs(PromptRPC, "ses_test", "text"),
		"path":    c.PromptArgs(PromptPath, "ses_test", "text"),
		"events":  c.EventArgs("session.execution.started"),
	} {
		if len(args) < 4 || args[1] != "--server" || args[2] != c.Server {
			t.Errorf("%s args missing --server URL: %q", name, args)
		}
	}
}

func TestPromptBothSyntaxesReturnSameAdmission(t *testing.T) {
	var calls [][]string
	c := &CLI{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		return []byte(`{"data":{"id":"msg_1","sessionID":"ses_test"}}`), nil
	}}
	for _, syntax := range []PromptSyntax{PromptRPC, PromptPath} {
		id, err := c.Prompt(context.Background(), syntax, "ses_test", "text")
		if err != nil {
			t.Fatalf("Prompt(%d): %v", syntax, err)
		}
		if id != "msg_1" {
			t.Fatalf("Prompt(%d) id = %q", syntax, id)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("runner calls = %d", len(calls))
	}
	if !reflect.DeepEqual(calls[0][len(calls[0])-1], calls[1][len(calls[1])-1]) {
		t.Fatalf("--data differs between syntaxes: %q vs %q", calls[0], calls[1])
	}
}

func TestPromptRejectsBadSessionAndMissingAck(t *testing.T) {
	c := &CLI{Runner: func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"data":{}}`), nil
	}}
	if _, err := c.Prompt(context.Background(), PromptPath, "not-a-session", "text"); err == nil {
		t.Fatal("invalid session id must be rejected")
	}
	if _, err := c.Prompt(context.Background(), PromptPath, "ses_test", "text"); err == nil {
		t.Fatal("ack without message id must be rejected")
	}
}

func TestPromptPropagatesRunnerError(t *testing.T) {
	c := &CLI{Runner: func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("exit 1")
	}}
	if _, err := c.Prompt(context.Background(), PromptRPC, "ses_test", "text"); err == nil {
		t.Fatal("runner error must be returned")
	}
}

func TestFramesParseAndDropPastEvents(t *testing.T) {
	raw := ": heartbeat\n" +
		"event: session.execution.started\n" +
		"data: {\"past\":true}\n\n" +
		"event: server.connected\n" +
		"data: {}\n\n" +
		"event: session.inbox.delivered\n" +
		"data: {\"id\":\"msg_1\"}\n\n" +
		"event: session.execution.started\n" +
		"data: {\"sessionID\":\"ses_test\"}"

	all := Frames([]byte(raw))
	if len(all) != 4 {
		t.Fatalf("frames = %d, want 4: %+v", len(all), all)
	}

	events := Events(all)
	if len(events) != 3 {
		t.Fatalf("events after connect = %d, want 3: %+v", len(events), events)
	}
	if events[0].Event != connectedEvent {
		t.Fatalf("first event = %q, want %q", events[0].Event, connectedEvent)
	}
	if events[1].Event != "session.inbox.delivered" {
		t.Fatalf("second event = %q", events[1].Event)
	}
	if !strings.Contains(events[2].Data, "ses_test") {
		t.Fatalf("last frame lost data: %q", events[2].Data)
	}
}

func TestSubscribeRequiresConnectionFrame(t *testing.T) {
	bad := &CLI{Runner: func(context.Context, ...string) ([]byte, error) {
		return []byte("event: session.execution.started\ndata: {}\n\n"), nil
	}}
	if _, err := bad.Subscribe(context.Background()); err == nil {
		t.Fatal("stream without server.connected must be rejected")
	}

	good := &CLI{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		if got := args[len(args)-1]; !strings.Contains(got, "/api/event?types=") {
			t.Fatalf("event args = %q", args)
		}
		return []byte("event: server.connected\ndata: {}\n\n"), nil
	}}
	frames, err := good.Subscribe(context.Background(), "session.execution.started")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if len(frames) != 1 || frames[0].Event != connectedEvent {
		t.Fatalf("frames = %+v", frames)
	}
}

func TestDeliverUsesEnvelope(t *testing.T) {
	var args []string
	c := &CLI{Runner: func(_ context.Context, a ...string) ([]byte, error) {
		args = a
		return []byte(`{"data":{"id":"msg_9"}}`), nil
	}}
	p := domain.Pointer{WatchID: "w", ObligationID: "o", Revision: 3}
	if err := c.Deliver(context.Background(), "ses_test", p); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	var sent struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(args[len(args)-1]), &sent); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if sent.Delivery != "steer" || sent.Text != Envelope(p) {
		t.Fatalf("delivery = %+v, want envelope %q", sent, Envelope(p))
	}
}

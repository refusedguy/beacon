package wake

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

func TestEnvelopeWhitelistsFields(t *testing.T) {
	p := domain.WakePointer{WatchID: "watch-1", ObligationID: "obl-1", Revision: 7, Verdict: "ready"}
	env := Envelope(p)
	if !strings.HasPrefix(env, "<beacon-event>") || !strings.HasSuffix(env, "</beacon-event>") {
		t.Fatalf("bad envelope: %q", env)
	}
	for _, want := range []string{`"watchID":"watch-1"`, `"obligationID":"obl-1"`, `"revision":7`, `"verdict":"ready"`} {
		if !strings.Contains(env, want) {
			t.Errorf("envelope missing %q: %s", want, env)
		}
	}
}

func TestOpenCodeDelivererPostsSteerPrompt(t *testing.T) {
	var gotArgs []string
	d := &OpenCodeDeliverer{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(`{"data":{"id":"msg_1","sessionID":"ses_test"}}`), nil
	}}
	err := d.Deliver(context.Background(), "ses_test", domain.WakePointer{
		WatchID: "watch-1", ObligationID: "obl-1", Revision: 1, Verdict: "ready",
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(gotArgs) < 6 {
		t.Fatalf("args = %q", gotArgs)
	}
	if gotArgs[0] != "api" || gotArgs[1] != "POST" || gotArgs[2] != "/api/session/ses_test/prompt" {
		t.Fatalf("unexpected api call: %q", gotArgs)
	}
	var sent struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(gotArgs[5]), &sent); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if sent.Delivery != "steer" {
		t.Fatalf("delivery = %q, want steer", sent.Delivery)
	}
	if !strings.HasPrefix(sent.Text, "<beacon-event>") || !strings.HasSuffix(sent.Text, "</beacon-event>") {
		t.Fatalf("missing envelope: %q", sent.Text)
	}
}

func TestOpenCodeDelivererRejectsBadSessionAndMissingAck(t *testing.T) {
	d := &OpenCodeDeliverer{Runner: func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"data":{}}`), nil
	}}
	if err := d.Deliver(context.Background(), "not-a-session", domain.WakePointer{}); err == nil {
		t.Fatal("invalid session id must be rejected")
	}
	if err := d.Deliver(context.Background(), "ses_test", domain.WakePointer{}); err == nil {
		t.Fatal("ack without message id must be rejected")
	}
}

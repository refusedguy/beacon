package wake

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestInboxParsesDeferredItem(t *testing.T) {
	a := &API{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		if args[1] != "GET" || args[2] != "/api/session/ses_test/inbox" {
			t.Fatalf("unexpected GET args: %q", args)
		}
		return []byte(`{"data":[{"id":"msg_456","delivery":"queue","text":"Recheck CI"}]}`), nil
	}}
	items, err := a.Inbox(context.Background(), "ses_test")
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(items) != 1 || items[0].ID != "msg_456" || items[0].Delivery != "queue" || items[0].Text != "Recheck CI" {
		t.Fatalf("items = %+v", items)
	}
}

func TestInboxAcceptsBareArray(t *testing.T) {
	a := &API{Runner: func(context.Context, ...string) ([]byte, error) {
		return []byte(`[{"id":"msg_1","delivery":"steer"}]`), nil
	}}
	items, err := a.Inbox(context.Background(), "ses_test")
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(items) != 1 || items[0].ID != "msg_1" {
		t.Fatalf("items = %+v", items)
	}
}

func TestFindInboxItem(t *testing.T) {
	items := []InboxItem{{ID: "msg_1"}, {ID: "msg_456"}}
	if got := FindInboxItem(items, "msg_456"); got == nil || got.ID != "msg_456" {
		t.Fatalf("FindInboxItem = %+v", got)
	}
	if got := FindInboxItem(items, "missing"); got != nil {
		t.Fatalf("missing id = %+v, want nil", got)
	}
	if got := FindInboxItem(items, ""); got != nil {
		t.Fatalf("empty id = %+v, want nil", got)
	}
}

func TestSteerPendingPatchesFoundItem(t *testing.T) {
	var calls [][]string
	a := &API{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[1] == "GET" {
			return []byte(`{"data":[{"id":"msg_456","delivery":"queue","text":"Recheck CI"}]}`), nil
		}
		return nil, nil // 204 has no body
	}}
	if err := a.SteerPending(context.Background(), "ses_test", "msg_456"); err != nil {
		t.Fatalf("SteerPending: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2 (GET then PATCH)", len(calls))
	}
	patch := calls[1]
	if patch[0] != "api" || patch[1] != "PATCH" || patch[2] != "/api/session/ses_test/inbox/msg_456" {
		t.Fatalf("unexpected PATCH args: %q", patch)
	}
	if patch[3] != "--data" {
		t.Fatalf("missing --data: %q", patch)
	}
	var sent struct {
		Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(patch[4]), &sent); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if sent.Delivery != deliverySteer {
		t.Fatalf("delivery = %q, want steer", sent.Delivery)
	}
}

func TestSteerPendingRejectsMissingItem(t *testing.T) {
	patched := false
	a := &API{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		if args[1] == "GET" {
			return []byte(`{"data":[]}`), nil
		}
		patched = true
		return nil, nil
	}}
	err := a.SteerPending(context.Background(), "ses_test", "msg_456")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
	if patched {
		t.Fatal("PATCH must not run for a missing item")
	}
}

func TestQueuePendingRestoresQueueWithoutText(t *testing.T) {
	var patch []string
	a := &API{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		patch = append([]string(nil), args...)
		return nil, nil
	}}
	if err := a.QueuePending(context.Background(), "ses_test", "msg_456"); err != nil {
		t.Fatalf("QueuePending: %v", err)
	}
	if patch[1] != "PATCH" || patch[2] != "/api/session/ses_test/inbox/msg_456" {
		t.Fatalf("unexpected PATCH args: %q", patch)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(patch[4]), &sent); err != nil {
		t.Fatalf("decode --data: %v", err)
	}
	if sent["delivery"] != deliveryQueue || len(sent) != 1 {
		t.Fatalf("payload = %v, want only delivery=queue", sent)
	}
}

func TestSetDeliveryValidatesInput(t *testing.T) {
	called := false
	a := &API{Runner: func(context.Context, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}}
	cases := []struct{ session, id, delivery string }{
		{"not-a-session", "msg_1", deliverySteer},
		{"ses_test", "", deliverySteer},
		{"ses_test", "msg_1", "unknown"},
	}
	for _, tc := range cases {
		if err := a.SetDelivery(context.Background(), tc.session, tc.id, tc.delivery); err == nil {
			t.Fatalf("SetDelivery(%q, %q, %q) must fail", tc.session, tc.id, tc.delivery)
		}
	}
	if called {
		t.Fatal("Runner must not be called for invalid input")
	}
}

func TestSetDeliveryPropagatesTransportError(t *testing.T) {
	a := &API{Runner: func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("api: 404 not found")
	}}
	err := a.SetDelivery(context.Background(), "ses_test", "msg_1", deliverySteer)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want wrapped 404", err)
	}
}

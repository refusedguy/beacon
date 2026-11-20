package wake

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPromptTargetDelivererCallsPromptWithSteer(t *testing.T) {
	var args []string
	api := &T202API{T202Runner: func(_ context.Context, call ...string) ([]byte, error) {
		args = call
		return []byte(`{"data":{"id":"m1"}}`), nil
	}}

	deliver := PromptTargetDeliverer(api)
	if err := deliver(context.Background(), "ses_owner", "identifier-only pointer"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(args) < 4 {
		t.Fatalf("runner args = %v, want an api POST call", args)
	}
	if args[0] != "api" || args[1] != "POST" || !strings.Contains(args[2], "/ses_owner/prompt") {
		t.Fatalf("runner args = %v, want prompt for ses_owner", args)
	}
	if !strings.Contains(args[4], `identifier-only pointer`) || !strings.Contains(args[4], `"delivery":"steer"`) {
		t.Fatalf("prompt body = %q, want steer pointer", args[4])
	}
}

func TestPromptTargetDelivererRefusesNonSessionTarget(t *testing.T) {
	called := false
	api := &T202API{T202Runner: func(context.Context, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}}
	deliver := PromptTargetDeliverer(api)

	if err := deliver(context.Background(), "beacon-daemon-checkall", "pointer"); err == nil {
		t.Fatal("a service name must not be delivered to")
	}
	if called {
		t.Fatal("no transport call is allowed for a non-session target")
	}
	if err := deliver(context.Background(), "ses_", "pointer"); err == nil {
		t.Fatal("a bare session prefix must not be delivered to")
	}
	if called {
		t.Fatal("no transport call is allowed for a bare session prefix")
	}
}

func TestPromptTargetDelivererReportsTransportError(t *testing.T) {
	boom := errors.New("transport refused")
	api := &T202API{T202Runner: func(context.Context, ...string) ([]byte, error) {
		return nil, boom
	}}
	if err := PromptTargetDeliverer(api)(context.Background(), "ses_owner", "pointer"); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
}

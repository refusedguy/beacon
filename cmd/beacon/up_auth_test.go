package main

import (
	"context"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/channel"
	"github.com/refusedguy/beacon/internal/infra/httpapi"
)

// upFakeResolver maps a fixed set of senders to actors. Every other sender is
// refused, like the operator-owned registry in server.go.
type upFakeResolver struct {
	actors map[string]domain.ActorID
}

func (r upFakeResolver) Resolve(_ context.Context, _ string, sender string) (domain.ActorID, error) {
	actor, ok := r.actors[sender]
	if !ok {
		return "", channel.ErrUnknownActor
	}
	return actor, nil
}

func installUpActorResolver(t *testing.T, resolver channel.ActorResolver) {
	t.Helper()
	previous := upActorResolver
	upActorResolver = resolver
	t.Cleanup(func() { upActorResolver = previous })
}

func TestHandleUpCommandSlashUnknownSenderRefused(t *testing.T) {
	installUpActorResolver(t, upFakeResolver{actors: map[string]domain.ActorID{"u-1": "ses_alice"}})
	messenger := &upFakeMessenger{}
	decisions := &upFakeDecisions{receipt: httpapi.ClientDecisionReceipt{OK: true, Applied: true}}

	handleUpCommand(context.Background(), messenger, "ghost", "/approve watch-1", upFakeStatus{}, decisions, nil)

	if len(decisions.requests) != 0 {
		t.Fatalf("decisions = %+v, want 0 for an unknown sender", decisions.requests)
	}
	if len(messenger.texts) != 1 || !strings.Contains(messenger.texts[0], "авторизован") {
		t.Fatalf("reply = %v, want a refusal", messenger.texts)
	}
}

func TestHandleUpCommandSlashKnownSenderCarriesActor(t *testing.T) {
	installUpActorResolver(t, upFakeResolver{actors: map[string]domain.ActorID{"u-1": "ses_alice"}})
	messenger := &upFakeMessenger{}
	decisions := &upFakeDecisions{receipt: httpapi.ClientDecisionReceipt{OK: true, Applied: true}}

	handleUpCommand(context.Background(), messenger, "u-1", "/approve watch-1", upFakeStatus{}, decisions, nil)

	if len(decisions.requests) != 1 {
		t.Fatalf("decisions = %d, want 1", len(decisions.requests))
	}
	if got := decisions.requests[0].Actor; got != "ses_alice" {
		t.Fatalf("actor = %q, want ses_alice", got)
	}
}

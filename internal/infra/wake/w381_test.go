package wake

import (
	"context"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

// Watch 360 has one executor subscriber, ses_e08778. A completed agent from
// another session must never receive a pointer that belongs to it.
const w381WatchID = "watch:refusedguy/beacon:360:c35188e350120913bc3b4d7b05f1aace2c22142d"

const (
	w381Executor    = "ses_e08778bafffegfXW4z5bgnTLHP"
	w381Orchestrator = "ses_e08f34cffffeQD2vSh7CXI3gqU"
)

func w381Watch() domain.Watch {
	return domain.Watch{
		ID: w381WatchID,
		Subscribers: []domain.Subscriber{
			{ID: w381Executor, Role: domain.RoleExecutor, Adapter: AdapterOpenCode},
			{ID: w381Orchestrator, Role: domain.RoleOrchestrator, Adapter: AdapterOpenCode},
		},
	}
}

type w381Call struct {
	session string
	payload domain.WakePayload
}

// Each subscriber gets only its own obligation. There is no broadcast and the
// caller's session id is never used as an address.
func TestW381DeliverToSubscribersHasNoCrosstalk(t *testing.T) {
	ctx := context.Background()
	w := w381Watch()
	var calls []w381Call
	d := &recordingWakeDeliverer{fn: func(_ context.Context, sessionID string, p domain.WakePayload) error {
		calls = append(calls, w381Call{session: sessionID, payload: p})
		return nil
	}}

	for i, sub := range []string{w381Executor, w381Orchestrator} {
		rev := uint64(7 + i)
		p := domain.WakePayload{
			WatchID:      w381WatchID,
			ObligationID: domain.ObligationKey(w381WatchID, rev, sub),
			Revision:     rev,
			Verdict:      "retry",
		}
		if err := DeliverToSubscriber(ctx, d, w, p, sub); err != nil {
			t.Fatalf("DeliverToSubscriber(%s): %v", sub, err)
		}
	}

	if len(calls) != 2 {
		t.Fatalf("deliveries = %d, want 2", len(calls))
	}
	for i, want := range []string{w381Executor, w381Orchestrator} {
		got := calls[i]
		if got.session != want {
			t.Fatalf("delivery %d went to %q, want %q", i, got.session, want)
		}
		wantObligation := domain.ObligationKey(w381WatchID, got.payload.Revision, want)
		if got.payload.ObligationID != wantObligation {
			t.Fatalf("delivery %d carried obligation %q, want %q", i, got.payload.ObligationID, wantObligation)
		}
		if got.payload.WatchID != w381WatchID {
			t.Fatalf("delivery %d carried watch %q", i, got.payload.WatchID)
		}
	}

	// A foreign obligation and a substituted caller session are both refused
	// before the deliverer is called again.
	foreign := domain.WakePayload{
		WatchID:      w381WatchID,
		ObligationID: domain.ObligationKey(w381WatchID, 7, w381Executor),
		Revision:     7,
	}
	if err := DeliverToSubscriber(ctx, d, w, foreign, w381Orchestrator); err == nil {
		t.Fatal("a foreign obligation must not wake another subscriber")
	}
	if err := DeliverToSubscriber(ctx, d, w, foreign, "ses_current_caller"); err == nil {
		t.Fatal("the current caller must not be used as a wake address")
	}
	if len(calls) != 2 {
		t.Fatalf("refused wakes produced %d extra deliveries", len(calls)-2)
	}
}

type w381Adapter struct {
	bound    []string
	delivers []string
	override string
}

func (a *w381Adapter) Bind(p T210WakePointer, _ *T211SessionRegistry) (T210Target, bool) {
	a.bound = append(a.bound, p.Target)
	if a.override != "" {
		return T210Target(a.override), true
	}
	return T210Target(p.Target), true
}

func (a *w381Adapter) Deliver(_ T210WakePointer, target T210Target) (T210Receipt, error) {
	a.delivers = append(a.delivers, string(target))
	return T210Receipt{Outcome: T210Delivered}, nil
}

// The universal bridge refuses a pointer whose target and obligation name
// different subscribers, and it refuses an adapter that binds another session.
func TestW381T210DeliverSkipsMismatchedAddresses(t *testing.T) {
	adapters := T211NewAdapterRegistry()
	a := &w381Adapter{}
	adapters.Register(AdapterOpenCode, a)

	foreign := T210WakePointer{
		Adapter:      AdapterOpenCode,
		Target:       w381Orchestrator,
		WatchID:      w381WatchID,
		ObligationID: domain.ObligationKey(w381WatchID, 1, w381Executor),
		Revision:     1,
	}
	if _, ok := T210Deliver(context.Background(), foreign, nil, nil, adapters, nil); ok {
		t.Fatal("a mismatched obligation was delivered")
	}
	if len(a.delivers) != 0 {
		t.Fatalf("mismatched pointer delivered to %v", a.delivers)
	}

	good := foreign
	good.Target = w381Executor
	if _, ok := T210Deliver(context.Background(), good, nil, nil, adapters, nil); !ok {
		t.Fatal("a valid pointer was skipped")
	}
	if len(a.delivers) != 1 || a.delivers[0] != w381Executor {
		t.Fatalf("valid deliveries = %v, want [%s]", a.delivers, w381Executor)
	}

	substituted := &w381Adapter{override: w381Orchestrator}
	adapters.Register(AdapterOpenCode, substituted)
	if _, ok := T210Deliver(context.Background(), good, nil, nil, adapters, nil); ok {
		t.Fatal("an adapter that substituted another session was accepted")
	}
	if len(substituted.delivers) != 0 {
		t.Fatalf("substituted target delivered to %v", substituted.delivers)
	}
}

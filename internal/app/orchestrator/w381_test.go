package orchestrator

import (
	"context"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/wake"
)

const (
	w381WatchID  = "watch:refusedguy/beacon:360:c35188e350120913bc3b4d7b05f1aace2c22142d"
	w381Sub      = "ses_e08778bafffegfXW4z5bgnTLHP"
	w381OtherSub = "ses_e08f34cffffeQD2vSh7CXI3gqU"
)

type w381RecordingAdapter struct{ targets []string }

func (a *w381RecordingAdapter) Bind(p wake.T210WakePointer, _ *wake.T211SessionRegistry) (wake.T210Target, bool) {
	return wake.T210Target(p.Target), true
}

func (a *w381RecordingAdapter) Deliver(_ wake.T210WakePointer, target wake.T210Target) (wake.T210Receipt, error) {
	a.targets = append(a.targets, string(target))
	return wake.T210Receipt{Outcome: wake.T210Delivered}, nil
}

// The daemon addresses the subscriber from its Watch lookup, never the session
// that happens to be calling at delivery time.
func TestW381DaemonWakerTargetsWatchSubscriber(t *testing.T) {
	adapters := wake.T211NewAdapterRegistry()
	a := &w381RecordingAdapter{}
	adapters.Register(wake.AdapterOpenCode, a)
	w := &daemonWaker{
		lookup: func(id string) (DaemonWatch, error) {
			return DaemonWatch{ID: id, PR: 360, Adapter: wake.AdapterOpenCode, SubscriberID: w381Sub}, nil
		},
		adapters: adapters,
	}

	if err := w.Wake(context.Background(), w381WatchID, 1, "retry"); err != nil {
		t.Fatalf("Wake: %v", err)
	}
	if len(a.targets) != 1 || a.targets[0] != w381Sub {
		t.Fatalf("wake targets = %v, want [%s]", a.targets, w381Sub)
	}
}

// If a lookup returns no subscriber, the daemon refuses to send anything.
func TestW381DaemonWakerSkipsEmptySubscriber(t *testing.T) {
	adapters := wake.T211NewAdapterRegistry()
	a := &w381RecordingAdapter{}
	adapters.Register(wake.AdapterOpenCode, a)
	w := &daemonWaker{
		lookup: func(id string) (DaemonWatch, error) {
			return DaemonWatch{ID: id, PR: 360, Adapter: wake.AdapterOpenCode}, nil
		},
		adapters: adapters,
	}

	if err := w.Wake(context.Background(), w381WatchID, 1, "retry"); err == nil {
		t.Fatal("wake without a subscriber must be refused")
	}
	if len(a.targets) != 0 {
		t.Fatalf("empty subscriber delivered to %v", a.targets)
	}
}

// The built-in adapter refuses a pointer whose target and obligation name
// different subscribers, so a substituted session cannot be prompted.
func TestW381DaemonAdapterRefusesForeignObligation(t *testing.T) {
	a := &daemonWakeAdapter{api: &wake.T202API{}}
	p := wake.T210WakePointer{
		Adapter:      wake.AdapterOpenCode,
		Target:       w381OtherSub,
		WatchID:      w381WatchID,
		ObligationID: daemonObligationKey(w381WatchID, 1, w381Sub),
		Revision:     1,
	}
	if _, ok := a.Bind(p, nil); ok {
		t.Fatal("adapter bound a target that does not own the obligation")
	}
}

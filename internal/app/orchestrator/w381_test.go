package orchestrator

import (
	"context"
	"testing"
)

const (
	w381WatchID = "watch:refusedguy/beacon:360:c35188e350120913bc3b4d7b05f1aace2c22142d"
	w381Sub     = "ses_e08778bafffegfXW4z5bgnTLHP"
)

type w381RecordingAdapter struct{ targets []string }

func (a *w381RecordingAdapter) Deliver(_ context.Context, p WakePointer) (WakeReceipt, bool) {
	a.targets = append(a.targets, p.Target)
	return WakeReceipt{Outcome: WakeDelivered}, true
}

// The daemon addresses the subscriber from its Watch lookup, never the session
// that happens to be calling at delivery time.
func TestW381DaemonWakerTargetsWatchSubscriber(t *testing.T) {
	a := &w381RecordingAdapter{}
	w := &daemonWaker{
		lookup: func(id string) (DaemonWatch, error) {
			return DaemonWatch{ID: id, PR: 360, Adapter: WakeDefaultAdapter, SubscriberID: w381Sub}, nil
		},
		backend: a,
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
	a := &w381RecordingAdapter{}
	w := &daemonWaker{
		lookup: func(id string) (DaemonWatch, error) {
			return DaemonWatch{ID: id, PR: 360, Adapter: WakeDefaultAdapter}, nil
		},
		backend: a,
	}

	if err := w.Wake(context.Background(), w381WatchID, 1, "retry"); err == nil {
		t.Fatal("wake without a subscriber must be refused")
	}
	if len(a.targets) != 0 {
		t.Fatalf("empty subscriber delivered to %v", a.targets)
	}
}

package cibridge

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
)

type w381WakeCall struct {
	session string
	payload domain.WakePayload
}

type w381Waker struct{ calls []w381WakeCall }

func (w *w381Waker) Deliver(_ context.Context, sessionID string, p domain.WakePayload) error {
	w.calls = append(w.calls, w381WakeCall{session: sessionID, payload: p})
	return nil
}

// A Watch with two wakeable subscribers and one non-session subscriber gets
// exactly two wakes. Each one carries the obligation of the session it was
// sent to, and neither session receives the other's pointer.
func TestW381BridgeWakesEachSubscriberOnlyItsObligation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const (
		watchID   = "watch:refusedguy/beacon:360:c35188e350120913bc3b4d7b05f1aace2c22142d"
		headSHA   = "c35188e350120913bc3b4d7b05f1aace2c22142d"
		executor  = "ses_e08778bafffegfXW4z5bgnTLHP"
		orchestr  = "ses_e08f34cffffeQD2vSh7CXI3gqU"
		notASess  = "notify-only"
	)
	w := domain.Watch{
		ID:      watchID,
		Repo:    "refusedguy/beacon",
		PR:      360,
		HeadSHA: headSHA,
		Status:  domain.StatusWatching,
		Verdict: domain.VerdictWatching,
		Subscribers: []domain.Subscriber{
			{ID: executor, Role: domain.RoleExecutor, Adapter: "opencode"},
			{ID: orchestr, Role: domain.RoleOrchestrator, Adapter: "opencode"},
			{ID: notASess, Role: domain.RoleExecutor, Adapter: "opencode"},
		},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}

	// A failing check yields a retry, a non-terminal verdict that must wake.
	state := domain.PRState{
		HeadRefOid: headSHA,
		Checks: map[string]domain.Check{
			"build": {Conclusion: "FAILURE", Required: true},
		},
	}
	waker := &w381Waker{}
	b := New(db, fakeCI{state: state}, fixedClock{at: time.Unix(1, 0)}, time.Second).WithWaker(waker)
	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}

	if len(waker.calls) != 2 {
		t.Fatalf("wakes = %d, want 2: %+v", len(waker.calls), waker.calls)
	}
	want := map[string]string{
		executor: domain.ObligationKey(watchID, 1, executor),
		orchestr: domain.ObligationKey(watchID, 1, orchestr),
	}
	seen := make(map[string]bool)
	for _, call := range waker.calls {
		obligation, ok := want[call.session]
		if !ok {
			t.Fatalf("wake went to unexpected session %q", call.session)
		}
		if seen[call.session] {
			t.Fatalf("session %q received two wakes", call.session)
		}
		seen[call.session] = true
		if call.payload.ObligationID != obligation {
			t.Fatalf("%s got obligation %q, want %q", call.session, call.payload.ObligationID, obligation)
		}
		if call.payload.WatchID != watchID || call.payload.Revision != 1 {
			t.Fatalf("%s got pointer %+v", call.session, call.payload)
		}
		o, err := db.Obligation(obligation)
		if err != nil {
			t.Fatalf("Obligation(%s): %v", obligation, err)
		}
		if o.SubscriberID != call.session {
			t.Fatalf("obligation addresses %q but wake went to %q", o.SubscriberID, call.session)
		}
	}
	for session := range want {
		if !seen[session] {
			t.Fatalf("subscriber %q received no wake", session)
		}
	}
}

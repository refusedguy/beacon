package cibridge

import (
	"context"
	"path/filepath"
	"strings"
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

// A failing check yields keep_working, whose only wake target is the executor.
// The orchestrator subscriber and the non-session subscriber get no wake, and
// the one delivery carries the executor's own obligation at the new revision.
func TestW381BridgeWakesTargetRoleOnlyItsObligation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const (
		watchID  = "watch:refusedguy/beacon:360:c35188e350120913bc3b4d7b05f1aace2c22142d"
		headSHA  = "c35188e350120913bc3b4d7b05f1aace2c22142d"
		executor = "ses_e08778bafffegfXW4z5bgnTLHP"
		orchestr = "ses_e08f34cffffeQD2vSh7CXI3gqU"
		notASess = "notify-only"
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

	if len(waker.calls) != 1 {
		t.Fatalf("wakes = %d, want 1: %+v", len(waker.calls), waker.calls)
	}
	call := waker.calls[0]
	if call.session != executor {
		t.Fatalf("wake went to %q, want executor %q", call.session, executor)
	}
	wantObligation := domain.ObligationKey(watchID, 1, executor)
	if call.payload.ObligationID != wantObligation {
		t.Fatalf("obligation = %q, want %q", call.payload.ObligationID, wantObligation)
	}
	if call.payload.WatchID != watchID || call.payload.Revision != 1 {
		t.Fatalf("pointer = %+v", call.payload)
	}
	if !strings.Contains(call.payload.Verdict, "keep_working") || !strings.Contains(call.payload.Verdict, "build") {
		t.Fatalf("wake text = %q, want keep_working with the failed check", call.payload.Verdict)
	}
	o, err := db.Obligation(wantObligation)
	if err != nil {
		t.Fatalf("Obligation(%s): %v", wantObligation, err)
	}
	if o.SubscriberID != executor {
		t.Fatalf("obligation addresses %q but wake went to %q", o.SubscriberID, executor)
	}
}

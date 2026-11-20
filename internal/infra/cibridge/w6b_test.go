package cibridge

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
)

type ladderCall struct {
	kind         string
	obligationID string
	verdict      domain.Verdict
	outcome      domain.AttemptOutcome
}

// recordingWakeLadder stands in for ladder.Ladder behind the domain-shaped
// port, so a test can see staging and delivery outcomes without SQL.
type recordingWakeLadder struct {
	calls []ladderCall
}

func (r *recordingWakeLadder) Stage(_ context.Context, o domain.Obligation, verdict domain.Verdict, _ time.Time) error {
	r.calls = append(r.calls, ladderCall{kind: "stage", obligationID: o.ID, verdict: verdict})
	return nil
}

func (r *recordingWakeLadder) Record(_ context.Context, id string, _ int, outcome domain.AttemptOutcome, _ time.Time) error {
	r.calls = append(r.calls, ladderCall{kind: "record", obligationID: id, outcome: outcome})
	return nil
}

// TestBridgeDrivesWakeLadder proves the bridge arms the ladder when it writes
// the obligation and reports a confirmed delivery as posted, which is the
// no-retry branch of ADR 0017.
func TestBridgeDrivesWakeLadder(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	w := domain.Watch{
		ID: "watch:w6b:1:abc", Repo: "o/r", PR: 1, HeadSHA: "abc",
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching, Revision: 1,
		Subscribers: []domain.Subscriber{
			{ID: "ses_orch", Role: domain.RoleOrchestrator, Adapter: "opencode"},
		},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	state := domain.PRState{
		HeadRefOid: "abc", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		Checks: map[string]domain.Check{"build": {Conclusion: "SUCCESS", Required: true}},
	}
	waker := &recordingAdapterWaker{}
	lad := &recordingWakeLadder{}
	b := New(db, fakeCI{state: state}, fixedClock{at: time.Unix(1, 0)}, time.Second).
		WithWaker(waker).WithLadder(lad)

	if got := b.Refresh(context.Background()); got != 1 {
		t.Fatalf("Refresh updated %d watches, want 1", got)
	}
	if len(waker.deliveries) != 1 {
		t.Fatalf("wake deliveries = %d, want 1", len(waker.deliveries))
	}

	staged, posted := 0, 0
	for _, c := range lad.calls {
		switch {
		case c.kind == "stage":
			staged++
			if c.verdict != domain.VerdictReady {
				t.Errorf("stage verdict = %q, want ready", c.verdict)
			}
		case c.kind == "record" && c.outcome == domain.OutcomePosted:
			posted++
		}
	}
	if staged != 1 || posted != 1 {
		t.Fatalf("ladder calls = %+v, want one stage and one posted record", lad.calls)
	}
}

// TestBridgeRecordsDeadSessionAsIndeterminate proves a dead session is never
// reported as a retryable transport failure.
func TestBridgeRecordsDeadSessionAsIndeterminate(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	w := domain.Watch{
		ID: "watch:w6b:2:abc", Repo: "o/r", PR: 2, HeadSHA: "abc",
		Status: domain.StatusWatching, Verdict: domain.VerdictWatching, Revision: 1,
		Subscribers: []domain.Subscriber{
			{ID: "ses_orch", Role: domain.RoleOrchestrator, Adapter: "opencode"},
		},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	state := domain.PRState{
		HeadRefOid: "abc", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		Checks: map[string]domain.Check{"build": {Conclusion: "SUCCESS", Required: true}},
	}
	waker := &recordingAdapterWaker{err: errors.New("session not found")}
	lad := &recordingWakeLadder{}
	b := New(db, fakeCI{state: state}, fixedClock{at: time.Unix(1, 0)}, time.Second).
		WithWaker(waker).WithLadder(lad)

	b.Refresh(context.Background())

	indeterminate, transport := 0, 0
	for _, c := range lad.calls {
		if c.kind != "record" {
			continue
		}
		if c.outcome == domain.OutcomeIndeterminate {
			indeterminate++
		}
		if c.outcome == domain.OutcomeTransportFailed {
			transport++
		}
	}
	if indeterminate < 1 || transport != 0 {
		t.Fatalf("outcomes = indeterminate %d transport %d, want >=1/0: %+v", indeterminate, transport, lad.calls)
	}
}

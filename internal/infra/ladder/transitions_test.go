package ladder

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
)

var base = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	d, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func newTestLadder(d *store.DB) *Ladder {
	return New(d, WithJitter(func(int) time.Duration { return 0 }))
}

func seedObligation(t *testing.T, d *store.DB, id, watchID string) domain.Obligation {
	t.Helper()
	o := domain.Obligation{
		ID:             id,
		WatchID:        watchID,
		Revision:       1,
		SubscriberID:   "sub-" + id,
		VerdictPointer: domain.VerdictReady,
		StagedAt:       base,
	}
	if err := d.SaveObligation(o); err != nil {
		t.Fatalf("SaveObligation(%s): %v", id, err)
	}
	return o
}

func stage(t *testing.T, l *Ladder, o domain.Obligation) {
	t.Helper()
	if err := l.Stage(context.Background(), o, domain.VerdictReady, base); err != nil {
		t.Fatalf("Stage: %v", err)
	}
}

func assertNoRetry(t *testing.T, due []Due) {
	t.Helper()
	for _, d := range due {
		if d.Kind == ActionRetry {
			t.Fatalf("unexpected retry due: %+v", d)
		}
	}
}

func TestPostedNoRepeats(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	o := seedObligation(t, d, "obl-posted", "watch-posted")
	stage(t, l, o)

	r := Receipt{ObligationID: o.ID, Attempt: 1, Outcome: domain.OutcomePosted, At: base.Add(time.Second)}
	if err := l.Record(ctx, r); err != nil {
		t.Fatalf("Record(posted): %v", err)
	}
	// A repeated receipt for the same attempt cannot add a second effect.
	if err := l.Record(ctx, r); err != nil {
		t.Fatalf("Record(posted) repeat: %v", err)
	}

	due, err := l.Due(ctx, base.Add(EvidenceWindow+time.Minute))
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	assertNoRetry(t, due)
	if len(due) != 1 || due[0].Kind != ActionEvidence {
		t.Fatalf("due = %+v, want one evidence action", due)
	}
	for len(due) > 0 {
		a, err := l.Advance(ctx, due[0].ObligationID, due[0].At)
		if err != nil {
			t.Fatalf("Advance: %v", err)
		}
		if a.Kind == ActionRetry {
			t.Fatalf("posted produced a retry: %+v", a)
		}
		due, err = l.Due(ctx, base.Add(60*time.Minute))
		if err != nil {
			t.Fatalf("Due: %v", err)
		}
	}

	got, err := d.Obligation(o.ID)
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if len(got.Attempts) != 1 || got.Attempts[0].Outcome != domain.OutcomePosted {
		t.Fatalf("attempts = %+v, want one posted attempt written in the transaction", got.Attempts)
	}
	m, err := l.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if m.Branch != domain.OutcomePosted || m.Status != StatusEscalated {
		t.Fatalf("marker = %+v, want posted/escalated", m)
	}
}

func TestTransportFailedCappedAtThree(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	o := seedObligation(t, d, "obl-transport", "watch-transport")
	stage(t, l, o)

	err := l.Record(ctx, Receipt{
		ObligationID: o.ID, Attempt: 1,
		Outcome: domain.OutcomeTransportFailed, At: base.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("Record(transport-failed): %v", err)
	}

	retries := 0
	for {
		due, err := l.Due(ctx, base.Add(60*time.Minute))
		if err != nil {
			t.Fatalf("Due: %v", err)
		}
		if len(due) == 0 {
			break
		}
		a, err := l.Advance(ctx, due[0].ObligationID, due[0].At)
		if err != nil {
			t.Fatalf("Advance: %v", err)
		}
		if a.ObligationID != o.ID || a.WatchID != o.WatchID {
			t.Fatalf("action ids = %q/%q, want %q/%q", a.ObligationID, a.WatchID, o.ID, o.WatchID)
		}
		switch a.Kind {
		case ActionRetry:
			retries++
			if retries > MaxAttempts {
				t.Fatalf("retries = %d, cap is %d", retries, MaxAttempts)
			}
			m, err := l.Position(ctx, o.ID)
			if err != nil {
				t.Fatalf("Position: %v", err)
			}
			if m.Attempts > MaxAttempts {
				t.Fatalf("attempts = %d, cap is %d", m.Attempts, MaxAttempts)
			}
		case ActionEvidence:
			// Evidence check owns the rest; no further retries are due.
		default:
			t.Fatalf("unexpected action %+v", a)
		}
	}
	if retries != 2 {
		t.Fatalf("retries = %d, want 2 (initial plus two repeats)", retries)
	}
	m, err := l.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if m.Attempts != MaxAttempts || m.Branch != domain.OutcomeTransportFailed {
		t.Fatalf("marker = %+v, want attempts=%d transport-failed", m, MaxAttempts)
	}
	if m.Status != StatusEscalated {
		t.Fatalf("status = %q, want escalated", m.Status)
	}
}

func TestIndeterminateNoRepeats(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	o := seedObligation(t, d, "obl-indeterminate", "watch-indeterminate")
	stage(t, l, o)

	err := l.Record(ctx, Receipt{
		ObligationID: o.ID, Attempt: 1,
		Outcome: domain.OutcomeIndeterminate, At: base.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("Record(indeterminate): %v", err)
	}

	due, err := l.Due(ctx, base.Add(EvidenceWindow+time.Minute))
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	assertNoRetry(t, due)
	if len(due) != 1 || due[0].Kind != ActionEvidence {
		t.Fatalf("due = %+v, want one evidence action", due)
	}
	m, err := l.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if m.Branch != domain.OutcomeIndeterminate {
		t.Fatalf("branch = %q, want indeterminate", m.Branch)
	}
}

func TestNoReceiptNoRepeats(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	o := seedObligation(t, d, "obl-no-receipt", "watch-no-receipt")
	stage(t, l, o)

	// No Record call at all: absence of a receipt is indeterminate, not failed.
	due, err := l.Due(ctx, base.Add(EvidenceWindow+time.Minute))
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	assertNoRetry(t, due)
	if len(due) != 1 || due[0].Kind != ActionEvidence {
		t.Fatalf("due = %+v, want one evidence action", due)
	}
	m, err := l.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if m.Branch != domain.OutcomeIndeterminate {
		t.Fatalf("branch = %q, want indeterminate", m.Branch)
	}
}

func TestRestartContinuesFromPosition(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	first := newTestLadder(d)
	o := seedObligation(t, d, "obl-restart", "watch-restart")
	stage(t, first, o)

	err := first.Record(ctx, Receipt{
		ObligationID: o.ID, Attempt: 1,
		Outcome: domain.OutcomeTransportFailed, At: base.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	a, err := first.Advance(ctx, o.ID, base.Add(FirstRetryDelay))
	if err != nil {
		t.Fatalf("Advance before restart: %v", err)
	}
	if a.Kind != ActionRetry || a.Attempt != 2 {
		t.Fatalf("action = %+v, want retry attempt 2", a)
	}

	// A brand-new Ladder over the same writer resumes from the marker.
	restarted := newTestLadder(d)
	m, err := restarted.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position after restart: %v", err)
	}
	if m.Attempts != 2 {
		t.Fatalf("attempts after restart = %d, want 2", m.Attempts)
	}
	a, err = restarted.Advance(ctx, o.ID, base.Add(SecondRetryDelay))
	if err != nil {
		t.Fatalf("Advance after restart: %v", err)
	}
	if a.Kind != ActionRetry || a.Attempt != MaxAttempts {
		t.Fatalf("action = %+v, want retry attempt %d", a, MaxAttempts)
	}

	due, err := restarted.Due(ctx, base.Add(60*time.Minute))
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	assertNoRetry(t, due)
	if len(due) != 1 || due[0].Kind != ActionEvidence {
		t.Fatalf("due = %+v, want one evidence action", due)
	}
	a, err = restarted.Advance(ctx, o.ID, due[0].At)
	if err != nil {
		t.Fatalf("Advance evidence: %v", err)
	}
	if a.Kind != ActionEvidence {
		t.Fatalf("action = %+v, want evidence", a)
	}
	m, err = restarted.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position final: %v", err)
	}
	if m.Attempts != MaxAttempts || m.Status != StatusEscalated {
		t.Fatalf("marker = %+v, want attempts=%d escalated", m, MaxAttempts)
	}
}

func TestReadStopsAllTimers(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	o := seedObligation(t, d, "obl-read", "watch-read")
	stage(t, l, o)
	if err := l.Record(ctx, Receipt{
		ObligationID: o.ID, Attempt: 1,
		Outcome: domain.OutcomePosted, At: base.Add(time.Second),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if err := l.MarkRead(ctx, o.ID, base.Add(time.Minute)); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	due, err := l.Due(ctx, base.Add(60*time.Minute))
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("due after read = %+v, want none", due)
	}
	m, err := l.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if m.Status != StatusStopped || !m.NextAt.IsZero() || !m.EvidenceAt.IsZero() {
		t.Fatalf("marker = %+v, want stopped with no timers", m)
	}
	got, err := d.Obligation(o.ID)
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if !got.ReadAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("ReadAt = %v, want %v", got.ReadAt, base.Add(time.Minute))
	}
}

func TestClaimedStopsAllTimers(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	o := seedObligation(t, d, "obl-claimed", "watch-claimed")
	stage(t, l, o)
	if err := l.MarkClaimed(ctx, o.ID, base.Add(time.Minute)); err != nil {
		t.Fatalf("MarkClaimed: %v", err)
	}
	due, err := l.Due(ctx, base.Add(60*time.Minute))
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("due after claimed = %+v, want none", due)
	}
	got, err := d.Obligation(o.ID)
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if !got.ClaimedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("ClaimedAt = %v, want %v", got.ClaimedAt, base.Add(time.Minute))
	}
}

func TestTimeExpiryAloneDoesNotCloseWatch(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	w := domain.Watch{
		ID: "watch-ttl", Repo: "o/r", PR: 1, HeadSHA: "abc123",
		Status: domain.StatusNotifying, Verdict: domain.VerdictReady, Revision: 1,
	}
	if err := d.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	o := seedObligation(t, d, "obl-ttl", w.ID)
	stage(t, l, o)

	// No receipt and no activity: only the evidence timer runs, out to well
	// past the 30-minute orphan threshold.
	for {
		due, err := l.Due(ctx, base.Add(120*time.Minute))
		if err != nil {
			t.Fatalf("Due: %v", err)
		}
		if len(due) == 0 {
			break
		}
		if _, err := l.Advance(ctx, due[0].ObligationID, due[0].At); err != nil {
			t.Fatalf("Advance: %v", err)
		}
	}

	got, err := d.Watch(w.ID)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got.Status != domain.StatusNotifying {
		t.Errorf("status = %q, want notifying: time expiry is not a close reason", got.Status)
	}
	if !got.ClosedAt.IsZero() || got.CloseReason != "" {
		t.Errorf("watch was closed: closedAt=%v reason=%q", got.ClosedAt, got.CloseReason)
	}
	m, err := l.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if m.Status != StatusEscalated {
		t.Fatalf("status = %q, want escalated (handed to T3.10)", m.Status)
	}
}

func TestRetryJitterWithinBounds(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := New(d) // default +/-30 second jitter
	lo := base.Add(FirstRetryDelay - MaxJitter)
	hi := base.Add(FirstRetryDelay + MaxJitter)

	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("obl-jitter-%d", i)
		o := seedObligation(t, d, id, "watch-jitter")
		stage(t, l, o)
		err := l.Record(ctx, Receipt{
			ObligationID: id, Attempt: 1,
			Outcome: domain.OutcomeTransportFailed, At: base.Add(time.Second),
		})
		if err != nil {
			t.Fatalf("Record: %v", err)
		}
		m, err := l.Position(ctx, id)
		if err != nil {
			t.Fatalf("Position: %v", err)
		}
		if m.NextAt.Before(lo) || m.NextAt.After(hi) {
			t.Fatalf("nextAt = %v, want within [%v, %v]", m.NextAt, lo, hi)
		}
	}
}

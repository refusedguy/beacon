package ladder

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

var base = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func newTestLadder(d *testDB) *Ladder {
	return New(d, WithJitter(func(int) time.Duration { return 0 }))
}

func seedObligation(t *testing.T, d *testDB, id, watchID string) domain.Obligation {
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

func TestFourDeliveryFailuresIssueOneHumanAlert(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	o := seedObligation(t, d, "obl-revoke", "watch-revoke")
	stage(t, l, o)

	// Staging starts at rung 1 with the initial pointer and no retry armed.
	got, err := d.Obligation(o.ID)
	if err != nil {
		t.Fatalf("Obligation after stage: %v", err)
	}
	if got.DeliveryRung != 1 || got.DeliveryAttempts != 1 || !got.NextRetryAt.IsZero() {
		t.Fatalf("staged markers = rung %d attempts %d next %v, want 1/1/zero",
			got.DeliveryRung, got.DeliveryAttempts, got.NextRetryAt)
	}

	// The revoke event is the domain boundary that carries a failed delivery
	// into the ladder: Transition stages one effect and leaves the Watch as is.
	w := domain.Watch{
		ID: o.WatchID, Status: domain.StatusNotifying,
		Verdict: domain.VerdictReady, Revision: 1,
	}
	boundary := domain.RevokeEvent(domain.DeliveryRevoke{
		ObligationID: o.ID, Outcome: domain.OutcomeTransportFailed,
	}, base)
	next, effects := domain.Transition(w, boundary)
	if len(effects) != 1 || effects[0].Kind != domain.EffectRevokeDelivery {
		t.Fatalf("revoke effects = %+v, want one revoke_delivery", effects)
	}
	if next.Status != w.Status || next.Revision != w.Revision {
		t.Fatalf("revoke changed the watch: %+v", next)
	}

	alerts, retries := 0, 0
	for i := 1; i <= 4; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		ev := domain.RevokeEvent(domain.DeliveryRevoke{
			ObligationID: o.ID, Outcome: domain.OutcomeTransportFailed,
		}, at)
		res, err := l.Revoke(ctx, *ev.Revoke, ev.At)
		if err != nil {
			t.Fatalf("Revoke %d: %v", i, err)
		}
		if res.ObligationID != o.ID || res.WatchID != o.WatchID {
			t.Fatalf("revoke %d ids = %q/%q, want %q/%q", i, res.ObligationID, res.WatchID, o.ID, o.WatchID)
		}
		if res.HumanAlert {
			alerts++
		}
		if res.Retry {
			retries++
		}

		switch i {
		case 1:
			if !res.HumanAlert {
				t.Fatal("first revoke must notify a human")
			}
			if !res.Retry || !res.NextAt.Equal(base.Add(FirstRetryDelay)) {
				t.Fatalf("first retry = %v/%v, want true/%v", res.Retry, res.NextAt, base.Add(FirstRetryDelay))
			}
		case 2:
			if res.HumanAlert {
				t.Fatal("second revoke must not notify a human twice")
			}
			if !res.Retry || !res.NextAt.Equal(base.Add(SecondRetryDelay)) {
				t.Fatalf("second retry = %v/%v, want true/%v", res.Retry, res.NextAt, base.Add(SecondRetryDelay))
			}
		default:
			if res.HumanAlert {
				t.Fatalf("revoke %d must not notify a human twice", i)
			}
			if res.Retry {
				t.Fatalf("revoke %d armed a retry after the cap of %d", i, MaxAttempts)
			}
		}
	}

	if alerts != 1 {
		t.Fatalf("human alerts = %d, want 1 for four failures", alerts)
	}
	if retries != 2 {
		t.Fatalf("retries = %d, want 2 within cap %d", retries, MaxAttempts)
	}

	got, err = d.Obligation(o.ID)
	if err != nil {
		t.Fatalf("Obligation after revokes: %v", err)
	}
	if got.DeliveryRung != MaxAttempts {
		t.Fatalf("delivery rung = %d, want cap %d", got.DeliveryRung, MaxAttempts)
	}
	if got.DeliveryAttempts != 5 {
		t.Fatalf("delivery attempts = %d, want 5 (initial pointer plus four failure reports)", got.DeliveryAttempts)
	}
	if !got.NextRetryAt.IsZero() {
		t.Fatalf("nextRetryAt = %v, want zero once the cap is spent", got.NextRetryAt)
	}
	m, err := l.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if !m.HumanAlerted {
		t.Fatal("marker must record that the human was alerted")
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

func TestGatePauseSuspendsLadder(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	o := seedObligation(t, d, "obl-gate-pause", "watch-gate-pause")
	stage(t, l, o)
	// A transport failure arms a future retry, so there is real work to suspend.
	if err := l.Record(ctx, Receipt{
		ObligationID: o.ID, Attempt: 1,
		Outcome: domain.OutcomeTransportFailed, At: base.Add(time.Second),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	before, err := l.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position before pause: %v", err)
	}
	armedAt := before.NextAt

	if err := l.Pause(ctx, o.ID, base.Add(2*time.Second)); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	m, err := l.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position after pause: %v", err)
	}
	if m.Status != StatusPaused {
		t.Fatalf("status = %q, want paused", m.Status)
	}
	if !m.NextAt.IsZero() {
		t.Fatalf("nextAt = %v, want cleared while paused", m.NextAt)
	}
	if m.Held == nil || !m.Held.NextAt.Equal(armedAt) ||
		m.Held.Branch != domain.OutcomeTransportFailed || m.Held.Attempts != 1 {
		t.Fatalf("held = %+v, want the armed retry position %v", m.Held, armedAt)
	}

	// No timer is due while a human reviews, and Advance does nothing.
	due, err := l.Due(ctx, base.Add(60*time.Minute))
	if err != nil {
		t.Fatalf("Due while paused: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("due while paused = %+v, want none", due)
	}
	a, err := l.Advance(ctx, o.ID, base.Add(60*time.Minute))
	if err != nil {
		t.Fatalf("Advance while paused: %v", err)
	}
	if a.Kind != "" {
		t.Fatalf("action while paused = %+v, want no-op", a)
	}
}

func TestGateApproveIsTerminal(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	o := seedObligation(t, d, "obl-gate-approve", "watch-gate-approve")
	stage(t, l, o)
	if err := l.Pause(ctx, o.ID, base.Add(time.Second)); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	if err := l.Ack(ctx, o.ID, GateDecision{
		Outcome: AckApprove, Reviewer: "human-1", At: base.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("Ack(approve): %v", err)
	}
	m, err := l.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if m.Status != StatusApproved || m.AckOutcome != AckApprove {
		t.Fatalf("marker = %+v, want approved", m)
	}
	if !m.NextAt.IsZero() || !m.EvidenceAt.IsZero() || m.Held != nil {
		t.Fatalf("marker = %+v, want no timers and no held position", m)
	}
	if m.AckReviewer != "human-1" {
		t.Fatalf("reviewer = %q, want human-1", m.AckReviewer)
	}
	due, err := l.Due(ctx, base.Add(60*time.Minute))
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("due after approve = %+v, want none", due)
	}
}

func TestGateContextResumesFromPausePoint(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	o := seedObligation(t, d, "obl-gate-context", "watch-gate-context")
	stage(t, l, o)
	if err := l.Record(ctx, Receipt{
		ObligationID: o.ID, Attempt: 1,
		Outcome: domain.OutcomeTransportFailed, At: base.Add(time.Second),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	armed, err := l.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position before pause: %v", err)
	}
	if err := l.Pause(ctx, o.ID, base.Add(2*time.Second)); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	if err := l.Ack(ctx, o.ID, GateDecision{
		Outcome: AckContext, Context: "proceed with the extra note",
		Reviewer: "human-2", At: base.Add(3 * time.Second),
	}); err != nil {
		t.Fatalf("Ack(context): %v", err)
	}
	m, err := l.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if m.Status != StatusActive || m.AckOutcome != AckContext {
		t.Fatalf("marker = %+v, want active with context ack", m)
	}
	if m.AckContext != "proceed with the extra note" || m.Held != nil {
		t.Fatalf("marker = %+v, want recorded context and cleared hold", m)
	}
	if !m.NextAt.Equal(armed.NextAt) || m.Attempts != armed.Attempts || m.Branch != armed.Branch {
		t.Fatalf("marker = %+v, want resumed at %v", m, armed.NextAt)
	}

	// The ladder continues at the exact point the gate interrupted: the armed
	// retry is due again, with the same attempt number.
	due, err := l.Due(ctx, base.Add(6*time.Minute))
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if len(due) != 1 || due[0].Kind != ActionRetry || due[0].Attempt != 2 {
		t.Fatalf("due = %+v, want one retry attempt 2", due)
	}
}

func TestGateReworkReturnsToStep(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	o := seedObligation(t, d, "obl-gate-rework", "watch-gate-rework")
	stage(t, l, o)
	if err := l.Pause(ctx, o.ID, base.Add(time.Second)); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	if err := l.Ack(ctx, o.ID, GateDecision{
		Outcome: AckRework, TargetStep: 3, Context: "redo from attempt three",
		Reviewer: "human-3", At: base.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("Ack(rework): %v", err)
	}
	m, err := l.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if m.Status != StatusActive || m.AckOutcome != AckRework || m.AckStep != 3 {
		t.Fatalf("marker = %+v, want active rework to step 3", m)
	}
	if m.Attempts != 2 || m.Branch != domain.OutcomeTransportFailed || m.Held != nil {
		t.Fatalf("marker = %+v, want retry branch poised for attempt 3", m)
	}
	if !m.NextAt.Equal(base.Add(SecondRetryDelay)) {
		t.Fatalf("nextAt = %v, want %v", m.NextAt, base.Add(SecondRetryDelay))
	}

	due, err := l.Due(ctx, base.Add(11*time.Minute))
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if len(due) != 1 || due[0].Kind != ActionRetry || due[0].Attempt != 3 {
		t.Fatalf("due = %+v, want one retry attempt 3", due)
	}
	a, err := l.Advance(ctx, o.ID, due[0].At)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if a.Kind != ActionRetry || a.Attempt != 3 {
		t.Fatalf("action = %+v, want retry attempt 3", a)
	}
}

func TestGateReworkToFirstStep(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	o := seedObligation(t, d, "obl-gate-rework-first", "watch-gate-rework-first")
	stage(t, l, o)
	if err := l.Pause(ctx, o.ID, base.Add(time.Second)); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	if err := l.Ack(ctx, o.ID, GateDecision{
		Outcome: AckRework, TargetStep: 1, At: base.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("Ack(rework step 1): %v", err)
	}
	m, err := l.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if m.Attempts != 0 || !m.NextAt.Equal(base.Add(2*time.Second)) {
		t.Fatalf("marker = %+v, want initial pointer re-sent at ack time", m)
	}
	due, err := l.Due(ctx, base.Add(2*time.Second))
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if len(due) != 1 || due[0].Attempt != 1 {
		t.Fatalf("due = %+v, want one retry attempt 1", due)
	}
}

func TestGateAckValidation(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	o := seedObligation(t, d, "obl-gate-validate", "watch-gate-validate")
	stage(t, l, o)
	if err := l.Pause(ctx, o.ID, base.Add(time.Second)); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	if err := l.Ack(ctx, o.ID, GateDecision{Outcome: "bogus"}); !errors.Is(err, ErrInvalidAckOutcome) {
		t.Fatalf("Ack(bogus) = %v, want ErrInvalidAckOutcome", err)
	}
	for _, step := range []int{0, -1, MaxAttempts + 1} {
		if err := l.Ack(ctx, o.ID, GateDecision{Outcome: AckRework, TargetStep: step}); !errors.Is(err, ErrInvalidGateStep) {
			t.Fatalf("Ack(rework step %d) = %v, want ErrInvalidGateStep", step, err)
		}
	}
	// Invalid decisions must not move the marker out of the gate.
	m, err := l.Position(ctx, o.ID)
	if err != nil {
		t.Fatalf("Position: %v", err)
	}
	if m.Status != StatusPaused {
		t.Fatalf("status = %q, want still paused after invalid acks", m.Status)
	}

	// An ack against an obligation that never paused is rejected.
	other := seedObligation(t, d, "obl-gate-notpaused", "watch-gate-notpaused")
	stage(t, l, other)
	if err := l.Ack(ctx, other.ID, GateDecision{Outcome: AckApprove}); !errors.Is(err, ErrGateNotPaused) {
		t.Fatalf("Ack(not paused) = %v, want ErrGateNotPaused", err)
	}
}

func TestGateReceiptIgnoredWhilePaused(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	o := seedObligation(t, d, "obl-gate-receipt", "watch-gate-receipt")
	stage(t, l, o)
	if err := l.Pause(ctx, o.ID, base.Add(time.Second)); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	if err := l.Record(ctx, Receipt{
		ObligationID: o.ID, Attempt: 1,
		Outcome: domain.OutcomePosted, At: base.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("Record while paused: %v", err)
	}
	got, err := d.Obligation(o.ID)
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if len(got.Attempts) != 0 {
		t.Fatalf("attempts while paused = %+v, want none recorded", got.Attempts)
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

func addReview(t *testing.T, l *Ladder, taskID string, reviews ...string) {
	t.Helper()
	for _, id := range reviews {
		if err := l.AddReview(context.Background(), taskID, id); err != nil {
			t.Fatalf("AddReview(%s): %v", id, err)
		}
	}
}

func TestQuorumCountsOnlyCompletedShipReviews(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	const task = "task-quorum-completed"
	addReview(t, l, task, "r1", "r2", "r3")

	// Only SHIP from a finished review is an approval marker. FIX-FIRST is a
	// finished review but not an approval; the third review is still running.
	if err := l.CompleteReview(ctx, task, "r1", ReviewShip); err != nil {
		t.Fatalf("CompleteReview(ship): %v", err)
	}
	if err := l.CompleteReview(ctx, task, "r2", ReviewFixFirst); err != nil {
		t.Fatalf("CompleteReview(fix-first): %v", err)
	}

	got, err := l.Quorum(ctx, task, 2)
	if err != nil {
		t.Fatalf("Quorum: %v", err)
	}
	if got.Result != QuorumPending || got.Approvals != 1 || got.MaxPossible != 2 {
		t.Fatalf("quorum = %+v, want pending 1/2", got)
	}

	// The running review finishes with SHIP and reaches the threshold.
	if err := l.CompleteReview(ctx, task, "r3", ReviewShip); err != nil {
		t.Fatalf("CompleteReview(ship r3): %v", err)
	}
	got, err = l.Quorum(ctx, task, 2)
	if err != nil {
		t.Fatalf("Quorum: %v", err)
	}
	if got.Result != QuorumReached || got.Approvals != 2 || got.MaxPossible != 2 {
		t.Fatalf("quorum = %+v, want reached 2/2", got)
	}
}

func TestQuorumRejectsUnreachableAfterAborts(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	const task = "task-quorum-unreachable"
	addReview(t, l, task, "r1", "r2", "r3")

	if err := l.CompleteReview(ctx, task, "r1", ReviewShip); err != nil {
		t.Fatalf("CompleteReview(ship): %v", err)
	}
	// Interrupted/incomplete/errored reviews yield no approval marker and are
	// removed from the pool of approvals still possible.
	if err := l.AbortReview(ctx, task, "r2"); err != nil {
		t.Fatalf("AbortReview(r2): %v", err)
	}
	if err := l.AbortReview(ctx, task, "r3"); err != nil {
		t.Fatalf("AbortReview(r3): %v", err)
	}

	got, err := l.Quorum(ctx, task, 3)
	if err != nil {
		t.Fatalf("Quorum: %v", err)
	}
	if got.Result != QuorumUnreachable || got.Approvals != 1 || got.MaxPossible != 1 {
		t.Fatalf("quorum = %+v, want unreachable 1/3", got)
	}
}

func TestQuorumBlockedVerdictIsNotAnApproval(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	const task = "task-quorum-blocked"
	addReview(t, l, task, "r1", "r2")

	if err := l.CompleteReview(ctx, task, "r1", ReviewShip); err != nil {
		t.Fatalf("CompleteReview(ship): %v", err)
	}
	if err := l.CompleteReview(ctx, task, "r2", ReviewBlocked); err != nil {
		t.Fatalf("CompleteReview(blocked): %v", err)
	}

	got, err := l.Quorum(ctx, task, 2)
	if err != nil {
		t.Fatalf("Quorum: %v", err)
	}
	if got.Result != QuorumUnreachable || got.Approvals != 1 || got.MaxPossible != 1 {
		t.Fatalf("quorum = %+v, want unreachable: BLOCKED is not an approval", got)
	}
}

func TestQuorumAddReviewIsIdempotent(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	const task = "task-quorum-idempotent"

	addReview(t, l, task, "r1")
	if err := l.CompleteReview(ctx, task, "r1", ReviewShip); err != nil {
		t.Fatalf("CompleteReview: %v", err)
	}
	addReview(t, l, task, "r1") // at-least-once caller must not reset the review

	got, err := l.Quorum(ctx, task, 1)
	if err != nil {
		t.Fatalf("Quorum: %v", err)
	}
	if got.Result != QuorumReached || got.Approvals != 1 {
		t.Fatalf("quorum = %+v, want reached with one approval after re-add", got)
	}
}

func TestQuorumReviewValidation(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	l := newTestLadder(d)
	const task = "task-quorum-validation"

	if err := l.CompleteReview(ctx, task, "gone", ReviewShip); !errors.Is(err, ErrReviewNotRunning) {
		t.Fatalf("CompleteReview(unknown) = %v, want ErrReviewNotRunning", err)
	}
	addReview(t, l, task, "r1")
	if err := l.CompleteReview(ctx, task, "r1", "SHIP?"); !errors.Is(err, ErrInvalidReviewVerdict) {
		t.Fatalf("CompleteReview(bogus) = %v, want ErrInvalidReviewVerdict", err)
	}
	if err := l.AbortReview(ctx, task, "r1"); err != nil {
		t.Fatalf("AbortReview: %v", err)
	}
	if err := l.CompleteReview(ctx, task, "r1", ReviewShip); !errors.Is(err, ErrReviewNotRunning) {
		t.Fatalf("CompleteReview(aborted) = %v, want ErrReviewNotRunning", err)
	}
	if _, err := l.Quorum(ctx, task, 0); !errors.Is(err, ErrInvalidThreshold) {
		t.Fatalf("Quorum(0) = %v, want ErrInvalidThreshold", err)
	}
}

func TestClassifyQuorumUnreachableTakesPriority(t *testing.T) {
	reviews := []ReviewMarker{
		{ReviewID: "a", State: ReviewDone, Verdict: ReviewShip},
		{ReviewID: "b", State: ReviewAborted},
		{ReviewID: "c", State: ReviewDone, Verdict: ReviewFixFirst},
	}
	got, err := classifyQuorum(reviews, 2)
	if err != nil {
		t.Fatalf("classifyQuorum: %v", err)
	}
	if got.Result != QuorumUnreachable || got.Approvals != 1 || got.MaxPossible != 1 || got.Threshold != 2 {
		t.Fatalf("quorum = %+v, want unreachable 1/2", got)
	}
}

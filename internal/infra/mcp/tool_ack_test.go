package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// TestAckSetsClaimedAtOnce repeats ack at a later time and checks that the
// first claimedAt survives and that a second write was not issued.
func TestAckSetsClaimedAtOnce(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 19, 11, 0, 0, 0, time.UTC)}
	w := resultFixture(domain.VerdictReady)
	o := resultObligation(w, "session-A")
	store := newMemStore()
	_ = store.SaveWatch(w)
	_ = store.SaveObligation(o)
	stamper := newRecordingStamper()
	stamper.add(o)
	h := NewAckHandler(store, stamper, clock)

	_, out, err := h.Handle(context.Background(), nil, AckInput{WatchID: w.ID, ObligationID: o.ID})
	if err != nil {
		t.Fatalf("first ack: %v", err)
	}
	if !out.OK {
		t.Fatal("first ack did not return ok")
	}
	first, _ := stamper.snapshot(o.ID)
	if !first.ClaimedAt.Equal(clock.t) {
		t.Fatalf("claimedAt = %v, want %v", first.ClaimedAt, clock.t)
	}

	clock.Advance(time.Hour)
	_, out, err = h.Handle(context.Background(), nil, AckInput{WatchID: w.ID, ObligationID: o.ID})
	if err != nil || !out.OK {
		t.Fatalf("repeat ack: out %+v err %v", out, err)
	}
	second, _ := stamper.snapshot(o.ID)
	if !second.ClaimedAt.Equal(first.ClaimedAt) {
		t.Errorf("claimedAt changed on repeat: %v -> %v", first.ClaimedAt, second.ClaimedAt)
	}
	reads, claims := stamper.counts()
	if reads != 0 || claims != 2 {
		t.Errorf("stamp calls = read %d claim %d, want 0 and 2", reads, claims)
	}
}

// t14LadderAdvance mirrors the wake-ledger rule from T1.4 / ADR 0017: a read or
// a claim stops the ladder, and only a transport-failed delivery may retry,
// within a three-attempt cap. It is used here only to observe the effect of
// get_result on the ladder.
func t14LadderAdvance(o domain.Obligation, outcome domain.AttemptOutcome, at time.Time) []domain.Attempt {
	if LadderStopped(o) {
		return o.Attempts
	}
	if len(o.Attempts) > 0 {
		last := o.Attempts[len(o.Attempts)-1].Outcome
		if last == domain.OutcomePosted || last == domain.OutcomeIndeterminate || len(o.Attempts) >= 3 {
			return o.Attempts
		}
	}
	return append(append([]domain.Attempt(nil), o.Attempts...), domain.Attempt{
		N:       len(o.Attempts) + 1,
		At:      at,
		Channel: "test",
		Outcome: outcome,
	})
}

// TestAckReadStopsLadder runs get_result before ack. Issuing the content sets
// readAt, which stops the wake ladder; ack then sets claimedAt without changing
// readAt and without resuming the ladder.
func TestAckReadStopsLadder(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	w := resultFixture(domain.VerdictKeepWorking)
	o := resultObligation(w, "session-L")
	o.Attempts = []domain.Attempt{{N: 1, At: clock.t, Channel: "s1", Outcome: domain.OutcomeTransportFailed}}
	store := newMemStore()
	_ = store.SaveWatch(w)
	_ = store.SaveObligation(o)
	stamper := newRecordingStamper()
	stamper.add(o)
	reader := NewGetResultHandler(store, stamper, clock)
	acker := NewAckHandler(store, stamper, clock)

	// A transport-failed delivery would retry while neither fact is present.
	retry := t14LadderAdvance(o, domain.OutcomeTransportFailed, clock.t.Add(time.Minute))
	if len(retry) != 2 {
		t.Fatalf("ladder with no read advanced to %d attempts, want 2", len(retry))
	}

	if _, err := reader.Result(context.Background(), sessionReq("session-L"), GetResultInput{WatchID: w.ID}); err != nil {
		t.Fatalf("get_result: %v", err)
	}
	read, _ := stamper.snapshot(o.ID)
	if read.ReadAt.IsZero() {
		t.Fatal("get_result did not stamp readAt")
	}
	if !LadderStopped(read) {
		t.Fatal("a read did not stop the ladder")
	}
	if after := t14LadderAdvance(read, domain.OutcomeTransportFailed, clock.t.Add(time.Minute)); len(after) != len(read.Attempts) {
		t.Errorf("ladder retried after read: %d attempts, want %d", len(after), len(read.Attempts))
	}

	if _, _, err := acker.Handle(context.Background(), nil, AckInput{WatchID: w.ID, ObligationID: o.ID}); err != nil {
		t.Fatalf("ack: %v", err)
	}
	claimed, _ := stamper.snapshot(o.ID)
	if claimed.ClaimedAt.IsZero() || !claimed.ReadAt.Equal(read.ReadAt) {
		t.Errorf("ack changed delivery facts: readAt %v claimedAt %v", claimed.ReadAt, claimed.ClaimedAt)
	}
	if !LadderStopped(claimed) {
		t.Error("ladder resumed after ack")
	}
}

// TestAckValidation covers bad input and a mismatched obligation before a
// stamp is issued.
func TestAckValidation(t *testing.T) {
	w := resultFixture(domain.VerdictReady)
	o := resultObligation(w, "session-V")
	store := newMemStore()
	_ = store.SaveWatch(w)
	_ = store.SaveObligation(o)
	stamper := newRecordingStamper()
	stamper.add(o)
	h := NewAckHandler(store, stamper, &stepClock{t: time.Now()})

	if err := h.Ack(context.Background(), AckInput{}); err == nil {
		t.Error("empty input was accepted")
	} else {
		assertCode(t, err, domain.CodeInvalidInput)
	}
	if err := h.Ack(context.Background(), AckInput{WatchID: "other", ObligationID: o.ID}); err == nil {
		t.Error("mismatched watch was accepted")
	} else {
		assertCode(t, err, domain.CodeInvalidInput)
	}
	if err := h.Ack(context.Background(), AckInput{WatchID: w.ID, ObligationID: "missing"}); err == nil {
		t.Error("missing obligation was accepted")
	} else {
		assertCode(t, err, domain.CodeWatchNotFound)
	}

	_, claims := stamper.counts()
	if claims != 0 {
		t.Errorf("a rejected ack stamped the obligation %d times", claims)
	}
}

// TestAckDoesNotChangeVerdict proves the ADR 0028 contract: an ack records a
// written intention without producing a terminal verdict. For a live Watch and
// for one that already has a verdict, ack writes claimedAt only. It leaves the
// Watch verdict and status untouched and returns an ok-only response with no
// verdict field. The verdict still has to be read from get_result or
// wait_result.
func TestAckDoesNotChangeVerdict(t *testing.T) {
	for _, verdict := range []domain.Verdict{domain.VerdictWatching, domain.VerdictReady} {
		t.Run(string(verdict), func(t *testing.T) {
			clock := &stepClock{t: time.Date(2026, 11, 19, 14, 0, 0, 0, time.UTC)}
			w := resultFixture(verdict)
			o := resultObligation(w, "session-V")
			store := newMemStore()
			if err := store.SaveWatch(w); err != nil {
				t.Fatalf("SaveWatch: %v", err)
			}
			if err := store.SaveObligation(o); err != nil {
				t.Fatalf("SaveObligation: %v", err)
			}
			stamper := newRecordingStamper()
			stamper.add(o)
			h := NewAckHandler(store, stamper, clock)

			_, out, err := h.Handle(context.Background(), nil, AckInput{WatchID: w.ID, ObligationID: o.ID})
			if err != nil {
				t.Fatalf("ack: %v", err)
			}
			if !out.OK {
				t.Fatal("ack did not return ok")
			}

			gotWatch, err := store.Watch(w.ID)
			if err != nil {
				t.Fatalf("read Watch after ack: %v", err)
			}
			if gotWatch.Verdict != verdict {
				t.Errorf("ack changed Watch verdict: %q -> %q", verdict, gotWatch.Verdict)
			}
			if gotWatch.Status != w.Status {
				t.Errorf("ack changed Watch status: %q -> %q", w.Status, gotWatch.Status)
			}

			gotObligation, err := store.Obligation(o.ID)
			if err != nil {
				t.Fatalf("read obligation after ack: %v", err)
			}
			if gotObligation.VerdictPointer != o.VerdictPointer {
				t.Errorf("ack changed verdict pointer: %q -> %q", o.VerdictPointer, gotObligation.VerdictPointer)
			}

			stamped, ok := stamper.snapshot(o.ID)
			if !ok {
				t.Fatal("ack did not stamp the obligation")
			}
			if stamped.ClaimedAt.IsZero() {
				t.Error("ack did not write claimedAt")
			}
			if stamped.VerdictPointer != o.VerdictPointer {
				t.Errorf("stamp changed verdict pointer: %q -> %q", o.VerdictPointer, stamped.VerdictPointer)
			}
			if !stamped.ReadAt.IsZero() {
				t.Errorf("ack stamped readAt %v, want zero", stamped.ReadAt)
			}

			body, err := json.Marshal(out)
			if err != nil {
				t.Fatalf("marshal ack response: %v", err)
			}
			var fields map[string]any
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatalf("unmarshal ack response: %v", err)
			}
			if _, ok := fields["verdict"]; ok {
				t.Errorf("ack response carries a verdict: %s", body)
			}
			if len(fields) != 1 || fields["ok"] != true {
				t.Errorf("ack response = %s, want only ok:true", body)
			}
		})
	}
}

// TestSQLObligationStamperClaimedAtIdempotent proves the production stamper
// persists claimedAt in one transaction and keeps it on a repeat.
func TestSQLObligationStamperClaimedAtIdempotent(t *testing.T) {
	o := resultObligation(resultFixture(domain.VerdictReady), "session-C")
	writer, load := newStampTestDB(t, o)
	stamper := NewSQLObligationStamper(writer)
	first := time.Date(2026, 11, 19, 13, 0, 0, 0, time.UTC)

	got, applied, err := stamper.StampClaimed(context.Background(), o.ID, first)
	if err != nil || !applied || !got.ClaimedAt.Equal(first) {
		t.Fatalf("first StampClaimed = %+v applied %v err %v", got, applied, err)
	}
	if stored := load(); !stored.ClaimedAt.Equal(first) {
		t.Errorf("persisted claimedAt = %v, want %v", stored.ClaimedAt, first)
	}

	_, applied, err = stamper.StampClaimed(context.Background(), o.ID, first.Add(time.Hour))
	if err != nil || applied {
		t.Fatalf("repeat StampClaimed applied %v err %v, want no write", applied, err)
	}
	if stored := load(); !stored.ClaimedAt.Equal(first) {
		t.Errorf("claimedAt overwritten on repeat: %v", stored.ClaimedAt)
	}
}

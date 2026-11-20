package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
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

// parkedAckWatch is a Watch whose agent asked a human a question.
func parkedAckWatch() domain.Watch {
	w := resultFixture(domain.VerdictNeedsInput)
	w.Status = domain.StatusNeedsInput
	return w
}

// TestAckInputSchemaHasContinueOf keeps continueOf in the derived MCP input
// schema, so a client can send a human answer back to the same task.
func TestAckInputSchemaHasContinueOf(t *testing.T) {
	schema, err := jsonschema.For[AckInput](nil)
	if err != nil {
		t.Fatalf("input schema: %v", err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"continueOf"`)) {
		t.Fatalf("input schema has no continueOf property: %s", raw)
	}
}

// TestAckContinueOfResumesSameWatch covers T3.21: an answer through
// ack(continueOf) resumes the parked Watch, still only records an intention and
// never creates a second task.
func TestAckContinueOfResumesSameWatch(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC)}
	w := parkedAckWatch()
	o := resultObligation(w, "session-answer")
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

	_, out, err := h.Handle(context.Background(), nil, AckInput{
		WatchID: w.ID, ObligationID: o.ID, ContinueOf: w.ID,
	})
	if err != nil || !out.OK {
		t.Fatalf("ack continueOf: out %+v err %v", out, err)
	}

	got, err := store.Watch(w.ID)
	if err != nil {
		t.Fatalf("read Watch after answer: %v", err)
	}
	if got.Status != domain.StatusWatching || got.Verdict != domain.VerdictWatching {
		t.Errorf("answer did not resume the Watch: status %q verdict %q", got.Status, got.Verdict)
	}
	if got.Revision != w.Revision+1 {
		t.Errorf("revision = %d, want %d", got.Revision, w.Revision+1)
	}
	if len(store.watches) != 1 {
		t.Errorf("answer created %d Watches, want 1", len(store.watches))
	}
	if _, err := store.Watch("w-new"); err == nil {
		t.Error("answer created a successor Watch")
	}

	stamped, ok := stamper.snapshot(o.ID)
	if !ok || stamped.ClaimedAt.IsZero() {
		t.Error("ack did not record the intention with claimedAt")
	}
	if !stamped.ReadAt.IsZero() {
		t.Errorf("ack stamped readAt %v, want zero", stamped.ReadAt)
	}
	if stamped.VerdictPointer != o.VerdictPointer {
		t.Errorf("ack changed the verdict pointer: %q -> %q", o.VerdictPointer, stamped.VerdictPointer)
	}
	if got := h.continuations.Rounds(w.ID); got != 1 {
		t.Errorf("rounds = %d, want 1", got)
	}
}

// TestAckContinueOfRoundLimit enforces the five-round cap from ADR 0031 §3.
// The sixth answer is refused before the Watch or the obligation changes.
func TestAckContinueOfRoundLimit(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)}
	w := parkedAckWatch()
	o := resultObligation(w, "session-limit")
	store := newMemStore()
	if err := store.SaveObligation(o); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}
	stamper := newRecordingStamper()
	stamper.add(o)
	h := NewAckHandler(store, stamper, clock)

	for round := 1; round <= int(domain.MaxContinueRounds)+1; round++ {
		parked := w
		parked.Revision = uint64(7 + round)
		if err := store.SaveWatch(parked); err != nil {
			t.Fatalf("SaveWatch for round %d: %v", round, err)
		}
		in := AckInput{WatchID: w.ID, ObligationID: o.ID, ContinueOf: w.ID}
		_, out, err := h.Handle(context.Background(), nil, in)
		if round <= int(domain.MaxContinueRounds) {
			if err != nil || !out.OK {
				t.Fatalf("round %d: out %+v err %v", round, out, err)
			}
			got, err := store.Watch(w.ID)
			if err != nil || got.Status != domain.StatusWatching {
				t.Fatalf("round %d did not resume: %+v err %v", round, got, err)
			}
			continue
		}
		if err == nil {
			t.Fatal("sixth answer was accepted")
		}
		assertCode(t, err, domain.CodeLimitExceeded)
		got, err := store.Watch(w.ID)
		if err != nil || got.Status != domain.StatusNeedsInput {
			t.Errorf("refused answer changed the Watch: %+v err %v", got, err)
		}
	}

	if got := h.continuations.Rounds(w.ID); got != domain.MaxContinueRounds {
		t.Errorf("rounds = %d, want %d", got, domain.MaxContinueRounds)
	}
	_, claims := stamper.counts()
	if claims != int(domain.MaxContinueRounds) {
		t.Errorf("claimed %d times, want %d (the refused round must not stamp)", claims, domain.MaxContinueRounds)
	}
	if len(store.watches) != 1 {
		t.Errorf("rounds created %d Watches, want 1", len(store.watches))
	}
}

// TestAckContinueOfMustNameSameWatch rejects an answer that points at another
// task, so a human reply can never start a new Watch.
func TestAckContinueOfMustNameSameWatch(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 20, 4, 0, 0, 0, time.UTC)}
	w := parkedAckWatch()
	o := resultObligation(w, "session-mismatch")
	store := newMemStore()
	_ = store.SaveWatch(w)
	_ = store.SaveObligation(o)
	stamper := newRecordingStamper()
	stamper.add(o)
	h := NewAckHandler(store, stamper, clock)

	_, _, err := h.Handle(context.Background(), nil, AckInput{
		WatchID: w.ID, ObligationID: o.ID, ContinueOf: "another-watch",
	})
	assertCode(t, err, domain.CodeInvalidInput)
	got, _ := store.Watch(w.ID)
	if got.Status != domain.StatusNeedsInput || got.Revision != w.Revision {
		t.Errorf("mismatched answer changed the Watch: %+v", got)
	}
	_, claims := stamper.counts()
	if claims != 0 {
		t.Errorf("rejected answer stamped the obligation %d times", claims)
	}
}

// TestSQLObligationStamperClaimedAtIdempotent proves the production stamper
// persists claimedAt in one transaction and keeps it on a repeat.
// TestAckInputSchemaHasRevision keeps the task revision in the derived MCP
// input schema, so a client can pin the version its event was made against.
func TestAckInputSchemaHasRevision(t *testing.T) {
	schema, err := jsonschema.For[AckInput](nil)
	if err != nil {
		t.Fatalf("input schema: %v", err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"revision"`)) {
		t.Fatalf("input schema has no revision property: %s", raw)
	}
}

// TestAckReceiptReturnsCommittedRevision proves ADR 0036 §5: the ack reports
// the Watch revision it was given at, not the webhook event.id of ADR 0034 §4.
// The ordinary MCP response stays ok-only as T2.23 requires.
func TestAckReceiptReturnsCommittedRevision(t *testing.T) {
	w := resultFixture(domain.VerdictReady)
	w.Revision = 7
	o := resultObligation(w, "session-revision")
	store := newMemStore()
	if err := store.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	if err := store.SaveObligation(o); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}
	stamper := newRecordingStamper()
	stamper.add(o)
	h := NewAckHandler(store, stamper, &stepClock{t: time.Now()})

	receipt, err := h.AckReceipt(context.Background(), AckInput{WatchID: w.ID, ObligationID: o.ID})
	if err != nil {
		t.Fatalf("AckReceipt: %v", err)
	}
	if !receipt.OK {
		t.Fatal("ack receipt is not ok")
	}
	if receipt.Revision != w.Revision {
		t.Errorf("receipt revision = %d, want committed Watch revision %d", receipt.Revision, w.Revision)
	}

	_, out, err := h.Handle(context.Background(), nil, AckInput{WatchID: w.ID, ObligationID: o.ID})
	if err != nil || !out.OK {
		t.Fatalf("ordinary ack Handle: out %+v err %v", out, err)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal ack response: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("unmarshal ack response: %v", err)
	}
	if len(fields) != 1 || fields["ok"] != true {
		t.Errorf("ack response = %s, want only ok:true", body)
	}
}

// TestAckDiscardsStaleEventRevision covers the acceptance criterion: once the
// Watch has moved to a newer revision, an event carrying the older revision is
// discarded. It stamps nothing, changes no Watch and reports the committed
// revision so the caller can retry against it.
func TestAckDiscardsStaleEventRevision(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 19, 16, 0, 0, 0, time.UTC)}
	w := resultFixture(domain.VerdictReady)
	w.Revision = 7
	o := resultObligation(w, "session-stale")
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

	first, err := h.AckReceipt(context.Background(), AckInput{WatchID: w.ID, ObligationID: o.ID, Revision: 7})
	if err != nil || first.Revision != 7 {
		t.Fatalf("first ack: receipt %+v err %v", first, err)
	}

	advanced := w
	advanced.Revision = 8
	if err := store.SaveWatch(advanced); err != nil {
		t.Fatalf("advance Watch: %v", err)
	}
	_, claimsBefore := stamper.counts()

	receipt, err := h.AckReceipt(context.Background(), AckInput{WatchID: w.ID, ObligationID: o.ID, Revision: 7})
	if !IsStaleRevision(err) {
		t.Fatalf("stale event was not discarded: receipt %+v err %v", receipt, err)
	}
	if receipt.OK {
		t.Error("stale event returned an ok receipt")
	}
	if receipt.Revision != 8 {
		t.Errorf("stale receipt revision = %d, want committed 8", receipt.Revision)
	}

	got, err := store.Watch(w.ID)
	if err != nil || got.Revision != 8 {
		t.Fatalf("stale event changed the Watch: %+v err %v", got, err)
	}
	_, claimsAfter := stamper.counts()
	if claimsAfter != claimsBefore {
		t.Errorf("stale event stamped the obligation: %d -> %d", claimsBefore, claimsAfter)
	}
	stamped, ok := stamper.snapshot(o.ID)
	if !ok || !stamped.ClaimedAt.Equal(clock.t) {
		t.Errorf("stale event changed claimedAt: %+v", stamped)
	}

	_, out, err := h.Handle(context.Background(), nil, AckInput{WatchID: w.ID, ObligationID: o.ID, Revision: 7})
	if err != nil {
		t.Fatalf("stale Handle returned an error: %v", err)
	}
	if out.OK {
		t.Error("stale Handle returned ok:true")
	}
}

// TestAckAcceptsCurrentAndFutureRevision keeps an ack whose event matches or
// leads the committed revision working, so only an older revision is dropped.
func TestAckAcceptsCurrentAndFutureRevision(t *testing.T) {
	w := resultFixture(domain.VerdictReady)
	w.Revision = 7
	o := resultObligation(w, "session-current")
	store := newMemStore()
	_ = store.SaveWatch(w)
	_ = store.SaveObligation(o)
	stamper := newRecordingStamper()
	stamper.add(o)
	h := NewAckHandler(store, stamper, &stepClock{t: time.Now()})

	for _, revision := range []uint64{7, 9} {
		receipt, err := h.AckReceipt(context.Background(), AckInput{WatchID: w.ID, ObligationID: o.ID, Revision: revision})
		if err != nil {
			t.Fatalf("revision %d: %v", revision, err)
		}
		if !receipt.OK || receipt.Revision != 7 {
			t.Errorf("revision %d: receipt %+v, want ok with committed 7", revision, receipt)
		}
	}
}

// TestAckStaleEventDoesNotResumeParkedWatch applies the same revision guard to
// an answer: a stale continueOf consumes no round and leaves the Watch parked.
func TestAckStaleEventDoesNotResumeParkedWatch(t *testing.T) {
	w := parkedAckWatch()
	w.Revision = 4
	o := resultObligation(w, "session-stale-answer")
	store := newMemStore()
	if err := store.SaveWatch(w); err != nil {
		t.Fatalf("SaveWatch: %v", err)
	}
	if err := store.SaveObligation(o); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}
	stamper := newRecordingStamper()
	stamper.add(o)
	h := NewAckHandler(store, stamper, &stepClock{t: time.Now()})

	receipt, err := h.AckReceipt(context.Background(), AckInput{
		WatchID: w.ID, ObligationID: o.ID, ContinueOf: w.ID, Revision: 3,
	})
	if !IsStaleRevision(err) {
		t.Fatalf("stale answer was not discarded: receipt %+v err %v", receipt, err)
	}

	got, err := store.Watch(w.ID)
	if err != nil || got.Status != domain.StatusNeedsInput || got.Revision != w.Revision {
		t.Errorf("stale answer changed the Watch: %+v err %v", got, err)
	}
	if rounds := h.continuations.Rounds(w.ID); rounds != 0 {
		t.Errorf("stale answer consumed %d rounds, want 0", rounds)
	}
	_, claims := stamper.counts()
	if claims != 0 {
		t.Errorf("stale answer stamped the obligation %d times", claims)
	}
}

// ackDecisionFixture wires a parked Watch and its obligation for a decision
// test. The Watch is stopped waiting for a human, which is the only state a
// decision can resume (ADR 0058 §4).
func ackDecisionFixture(t *testing.T) (*memStore, *recordingStamper, *AckHandler, domain.Watch, domain.Obligation, *stepClock) {
	t.Helper()
	clock := &stepClock{t: time.Date(2026, 11, 20, 6, 0, 0, 0, time.UTC)}
	w := parkedAckWatch()
	o := resultObligation(w, "session-decision")
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
	return store, stamper, h, w, o, clock
}

// TestDecisionValidSet keeps approve and reject the only accepted decisions.
func TestDecisionValidSet(t *testing.T) {
	if !DecisionApprove.Valid() || !DecisionReject.Valid() {
		t.Fatal("approve and reject must be valid decisions")
	}
	for _, bad := range []Decision{"", "maybe", "APPROVE", "approve "} {
		if bad.Valid() {
			t.Errorf("decision %q must not be valid", bad)
		}
	}
}

// TestAckInputSchemaHasDecision keeps the decision fields in the derived MCP
// input schema, so a client can send approve or reject through beacon_ack.
func TestAckInputSchemaHasDecision(t *testing.T) {
	schema, err := jsonschema.For[AckInput](nil)
	if err != nil {
		t.Fatalf("input schema: %v", err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	for _, field := range []string{"decision", "decisionID"} {
		if !bytes.Contains(raw, []byte(`"`+field+`"`)) {
			t.Fatalf("input schema has no %s property: %s", field, raw)
		}
	}
}

// TestAckDecisionResumesStoppedAgent is the acceptance test for ADR 0058 §4:
// approve or reject resumes the stopped agent in the same Watch. Both values
// resume; the decision is recorded so the client can prove which answer was
// applied. No second Watch is created and the plain ack still stamps claimedAt
// without a verdict pointer.
func TestAckDecisionResumesStoppedAgent(t *testing.T) {
	for _, decision := range []Decision{DecisionApprove, DecisionReject} {
		t.Run(string(decision), func(t *testing.T) {
			store, stamper, h, w, o, clock := ackDecisionFixture(t)

			receipt, err := h.Decide(context.Background(), AckInput{
				WatchID: w.ID, ObligationID: o.ID,
			}, decision, "decision-1")
			if err != nil {
				t.Fatalf("decide: %v", err)
			}
			if !receipt.OK {
				t.Fatal("decision receipt is not ok")
			}
			if receipt.Decision != decision || receipt.DecisionID != "decision-1" {
				t.Errorf("receipt decision = %+v, want %q/%q", receipt, decision, "decision-1")
			}
			if !receipt.Resumed {
				t.Error("decision did not resume the agent")
			}
			if receipt.Revision != w.Revision {
				t.Errorf("receipt revision = %d, want committed %d", receipt.Revision, w.Revision)
			}
			if receipt.ResumeRevision != w.Revision+1 {
				t.Errorf("resume revision = %d, want %d", receipt.ResumeRevision, w.Revision+1)
			}

			got, err := store.Watch(w.ID)
			if err != nil {
				t.Fatalf("read Watch after decision: %v", err)
			}
			if got.Status != domain.StatusWatching || got.Verdict != domain.VerdictWatching {
				t.Errorf("decision did not resume the Watch: status %q verdict %q", got.Status, got.Verdict)
			}
			if got.Revision != w.Revision+1 {
				t.Errorf("revision = %d, want %d", got.Revision, w.Revision+1)
			}
			if len(store.watches) != 1 {
				t.Errorf("decision created %d Watches, want 1", len(store.watches))
			}

			rec, ok := h.decisions.Lookup(w.ID)
			if !ok {
				t.Fatal("decision was not recorded")
			}
			if rec.Decision != decision || rec.DecisionID != "decision-1" {
				t.Errorf("recorded decision = %+v, want %q/%q", rec, decision, "decision-1")
			}
			if !rec.ResumedAt.Equal(clock.t) {
				t.Errorf("resumedAt = %v, want %v", rec.ResumedAt, clock.t)
			}

			stamped, ok := stamper.snapshot(o.ID)
			if !ok || stamped.ClaimedAt.IsZero() {
				t.Error("decision ack did not record the intention with claimedAt")
			}
			if stamped.VerdictPointer != o.VerdictPointer {
				t.Errorf("decision changed the verdict pointer: %q -> %q", o.VerdictPointer, stamped.VerdictPointer)
			}
		})
	}
}

// TestAckDecisionIdempotent proves a repeated decision resumes at most once.
// The second identical decision is a no-op: it does not advance the Watch, does
// not overwrite claimedAt and reports Resumed=false. The stamper is still
// called again, as with any repeated ack; its write stays idempotent.
func TestAckDecisionIdempotent(t *testing.T) {
	store, stamper, h, w, o, clock := ackDecisionFixture(t)
	in := AckInput{WatchID: w.ID, ObligationID: o.ID, Decision: string(DecisionApprove), DecisionID: "decision-1"}

	first, err := h.AckReceipt(context.Background(), in)
	if err != nil || !first.Resumed {
		t.Fatalf("first decision: receipt %+v err %v", first, err)
	}
	stampedFirst, ok := stamper.snapshot(o.ID)
	if !ok {
		t.Fatal("first decision did not stamp claimedAt")
	}
	clock.Advance(time.Hour)

	second, err := h.AckReceipt(context.Background(), in)
	if err != nil {
		t.Fatalf("repeat decision: %v", err)
	}
	if !second.OK || second.Decision != DecisionApprove || second.Resumed {
		t.Errorf("repeat decision = %+v, want ok approve without resume", second)
	}
	if second.ResumeRevision != first.ResumeRevision {
		t.Errorf("repeat resume revision = %d, want %d", second.ResumeRevision, first.ResumeRevision)
	}

	got, err := store.Watch(w.ID)
	if err != nil || got.Revision != w.Revision+1 || got.Status != domain.StatusWatching {
		t.Errorf("repeat decision changed the Watch: %+v err %v", got, err)
	}
	stampedSecond, ok := stamper.snapshot(o.ID)
	if !ok || !stampedSecond.ClaimedAt.Equal(stampedFirst.ClaimedAt) {
		t.Errorf("repeat decision changed claimedAt: %v -> %v", stampedFirst.ClaimedAt, stampedSecond.ClaimedAt)
	}
}

// TestAckDecisionConflictRejected refuses a second, different decision for a
// Watch that was already answered. The first decision stays authoritative.
func TestAckDecisionConflictRejected(t *testing.T) {
	store, _, h, w, o, _ := ackDecisionFixture(t)
	if _, err := h.Decide(context.Background(), AckInput{
		WatchID: w.ID, ObligationID: o.ID,
	}, DecisionApprove, "decision-1"); err != nil {
		t.Fatalf("first decision: %v", err)
	}

	_, err := h.Decide(context.Background(), AckInput{
		WatchID: w.ID, ObligationID: o.ID,
	}, DecisionReject, "decision-2")
	if err == nil {
		t.Fatal("a conflicting second decision was accepted")
	}
	assertCode(t, err, domain.CodeInvalidInput)

	got, err := store.Watch(w.ID)
	if err != nil || got.Revision != w.Revision+1 {
		t.Errorf("conflicting decision changed the Watch: %+v err %v", got, err)
	}
	rec, ok := h.decisions.Lookup(w.ID)
	if !ok || rec.Decision != DecisionApprove {
		t.Errorf("conflicting decision overwrote the first: %+v ok %v", rec, ok)
	}
}

// TestAckDecisionRequiresStoppedWatch keeps a decision scoped to an agent that
// actually stopped. A live Watch cannot be resumed and is left untouched.
func TestAckDecisionRequiresStoppedWatch(t *testing.T) {
	w := resultFixture(domain.VerdictReady)
	o := resultObligation(w, "session-live")
	store := newMemStore()
	_ = store.SaveWatch(w)
	_ = store.SaveObligation(o)
	stamper := newRecordingStamper()
	stamper.add(o)
	h := NewAckHandler(store, stamper, &stepClock{t: time.Now()})

	_, err := h.Decide(context.Background(), AckInput{
		WatchID: w.ID, ObligationID: o.ID,
	}, DecisionApprove, "decision-live")
	if err == nil {
		t.Fatal("decision on a live Watch was accepted")
	}
	assertCode(t, err, domain.CodeInvalidInput)

	got, _ := store.Watch(w.ID)
	if got.Status != w.Status || got.Revision != w.Revision {
		t.Errorf("decision changed a live Watch: %+v", got)
	}
	_, claims := stamper.counts()
	if claims != 0 {
		t.Errorf("rejected decision stamped the obligation %d times", claims)
	}
}

// TestAckDecisionRejectsInvalidValue confines decisions to the closed set.
func TestAckDecisionRejectsInvalidValue(t *testing.T) {
	_, _, h, w, o, _ := ackDecisionFixture(t)
	_, err := h.AckReceipt(context.Background(), AckInput{
		WatchID: w.ID, ObligationID: o.ID, Decision: "maybe",
	})
	if err == nil {
		t.Fatal("an unknown decision was accepted")
	}
	assertCode(t, err, domain.CodeInvalidInput)
}

// TestAckDecisionCannotCombineWithContinueOf keeps the two additive ack modes
// separate: a human answer round and an approve/reject decision are not mixed.
func TestAckDecisionCannotCombineWithContinueOf(t *testing.T) {
	store, _, h, w, o, _ := ackDecisionFixture(t)
	_, err := h.AckReceipt(context.Background(), AckInput{
		WatchID: w.ID, ObligationID: o.ID, Decision: string(DecisionApprove), ContinueOf: w.ID,
	})
	if err == nil {
		t.Fatal("decision combined with continueOf was accepted")
	}
	assertCode(t, err, domain.CodeInvalidInput)
	got, _ := store.Watch(w.ID)
	if got.Status != domain.StatusNeedsInput {
		t.Errorf("rejected combination changed the Watch: %+v", got)
	}
}

// TestAckDecisionStaleRevisionDiscarded applies the ADR 0036 §5 guard to a
// decision: a stale event resumes nothing, records no decision and stamps no
// claimedAt.
func TestAckDecisionStaleRevisionDiscarded(t *testing.T) {
	store, stamper, h, w, o, _ := ackDecisionFixture(t)
	in := AckInput{
		WatchID: w.ID, ObligationID: o.ID,
		Decision: string(DecisionApprove), DecisionID: "decision-stale", Revision: w.Revision - 1,
	}
	receipt, err := h.AckReceipt(context.Background(), in)
	if !IsStaleRevision(err) {
		t.Fatalf("stale decision was not discarded: receipt %+v err %v", receipt, err)
	}
	got, err := store.Watch(w.ID)
	if err != nil || got.Status != domain.StatusNeedsInput || got.Revision != w.Revision {
		t.Errorf("stale decision changed the Watch: %+v err %v", got, err)
	}
	if _, ok := h.decisions.Lookup(w.ID); ok {
		t.Error("stale decision was recorded")
	}
	_, claims := stamper.counts()
	if claims != 0 {
		t.Errorf("stale decision stamped the obligation %d times", claims)
	}
}

// TestAckDecisionHandleStaysOkOnly proves the decision keeps the ADR 0028
// response contract: the MCP tool still returns ok-only with no verdict. The
// decision detail is read from the receipt, not from the tool response.
func TestAckDecisionHandleStaysOkOnly(t *testing.T) {
	_, _, h, w, o, _ := ackDecisionFixture(t)
	_, out, err := h.Handle(context.Background(), nil, AckInput{
		WatchID: w.ID, ObligationID: o.ID, Decision: string(DecisionApprove), DecisionID: "decision-1",
	})
	if err != nil {
		t.Fatalf("Handle with decision: %v", err)
	}
	if !out.OK {
		t.Fatal("Handle with decision did not return ok")
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(fields) != 1 || fields["ok"] != true {
		t.Errorf("decision ack response = %s, want only ok:true", body)
	}
}

// sortedCheckNames and sortedCheckNamesFromInfo turn the two failed-check
// projections into comparable slices for the block payload assertions.
func sortedCheckNames(in []FailedCheck) []string {
	out := failedCheckNames(in)
	sort.Strings(out)
	return out
}

func sortedCheckNamesFromInfo(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// TestAckBlockedNotMine proves that a blocked:not_mine decision blocks the
// Watch in place. The block carries the failed checks from the Watch, the ack
// time and the session ID from _meta. No new Watch is created and the ack still
// records the claim. The MCP response stays ok-only.
func TestAckBlockedNotMine(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)}
	w := resultFixture(domain.VerdictRetry)
	w.Status = domain.StatusWatching
	o := resultObligation(w, "session-blocked")
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

	_, out, err := h.Handle(context.Background(), sessionReq("session-blocker"), AckInput{
		WatchID: w.ID, ObligationID: o.ID, Decision: string(DecisionBlockedNotMine),
	})
	if err != nil || !out.OK {
		t.Fatalf("blocked ack: out %+v err %v", out, err)
	}

	got, err := store.Watch(w.ID)
	if err != nil {
		t.Fatalf("read Watch after block: %v", err)
	}
	if got.Status != domain.StatusBlocked || got.Verdict != domain.VerdictBlocked {
		t.Errorf("block did not change Watch: status %q verdict %q", got.Status, got.Verdict)
	}
	if got.Revision != w.Revision+1 {
		t.Errorf("revision = %d, want %d", got.Revision, w.Revision+1)
	}
	if len(store.watches) != 1 {
		t.Errorf("block created %d Watches, want 1", len(store.watches))
	}
	// The payload bug: status and verdict moved but Blocked stayed nil. Every
	// reader of the Watch must now see the block details, not just the registry.
	if got.Blocked == nil {
		t.Fatal("block did not populate Watch.Blocked")
	}
	if got.Blocked.Reason != "not_mine" {
		t.Errorf("Watch.Blocked.Reason = %q, want not_mine", got.Blocked.Reason)
	}
	if !got.Blocked.Since.Equal(clock.t) {
		t.Errorf("Watch.Blocked.Since = %v, want %v", got.Blocked.Since, clock.t)
	}
	if got.Blocked.BlockedBy != "session-blocker" {
		t.Errorf("Watch.Blocked.BlockedBy = %q, want session-blocker", got.Blocked.BlockedBy)
	}
	wantNames := sortedCheckNames(collectFailedChecks(w))
	if gotNames := sortedCheckNamesFromInfo(got.Blocked.FailedChecks); !reflect.DeepEqual(gotNames, wantNames) {
		t.Errorf("Watch.Blocked.FailedChecks = %v, want %v", gotNames, wantNames)
	}

	rec, ok := h.blocked.Lookup(w.ID)
	if !ok {
		t.Fatal("block was not recorded")
	}
	if rec.WatchID != w.ID || rec.Reason != "not_mine" {
		t.Errorf("block record = %+v, want watch %q reason not_mine", rec, w.ID)
	}
	if !rec.Since.Equal(clock.t) {
		t.Errorf("block since = %v, want %v", rec.Since, clock.t)
	}
	if rec.BlockedBy != "session-blocker" {
		t.Errorf("blockedBy = %q, want session-blocker", rec.BlockedBy)
	}
	want := sortedFailedChecks(collectFailedChecks(w))
	if got := sortedFailedChecks(rec.FailedChecks); !reflect.DeepEqual(got, want) {
		t.Errorf("block failedChecks = %+v, want %+v", got, want)
	}

	stamped, ok := stamper.snapshot(o.ID)
	if !ok || stamped.ClaimedAt.IsZero() {
		t.Error("blocked ack did not record the intention with claimedAt")
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(fields) != 1 || fields["ok"] != true {
		t.Errorf("blocked ack response = %s, want only ok:true", body)
	}
}

// TestAckBlockedUnknownDecisionDoesNotBlock keeps the blocked family closed.
// An unknown blocked: reason and an unknown decision are both refused before a
// block, a Watch write or a claim happens.
func TestAckBlockedUnknownDecisionDoesNotBlock(t *testing.T) {
	for _, decision := range []string{"blocked:other", "maybe", "blocked:"} {
		t.Run(decision, func(t *testing.T) {
			clock := &stepClock{t: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)}
			w := resultFixture(domain.VerdictRetry)
			o := resultObligation(w, "session-unknown")
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

			_, _, err := h.Handle(context.Background(), sessionReq("session-blocker"), AckInput{
				WatchID: w.ID, ObligationID: o.ID, Decision: decision,
			})
			if err == nil {
				t.Fatalf("unknown decision %q was accepted", decision)
			}
			assertCode(t, err, domain.CodeInvalidInput)

			got, err := store.Watch(w.ID)
			if err != nil || got.Status != w.Status || got.Revision != w.Revision {
				t.Errorf("unknown decision changed the Watch: %+v err %v", got, err)
			}
			if _, ok := h.blocked.Lookup(w.ID); ok {
				t.Error("unknown decision recorded a block")
			}
			_, claims := stamper.counts()
			if claims != 0 {
				t.Errorf("unknown decision stamped the obligation %d times", claims)
			}
		})
	}
}

// TestAckBlockedNotMineIdempotent proves a repeat block cannot move the clock
// or rename the blocker. The first record wins and the Watch is written once.
func TestAckBlockedNotMineIdempotent(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
	w := resultFixture(domain.VerdictRetry)
	o := resultObligation(w, "session-repeat")
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
	in := AckInput{WatchID: w.ID, ObligationID: o.ID, Decision: string(DecisionBlockedNotMine)}

	// AckReceipt has no _meta; the block needs a session, so use the path the
	// MCP handler takes with the caller's session ID.
	first, err := h.ackReceiptWithSession(context.Background(), in, "session-first")
	if err != nil || !first.Blocked {
		t.Fatalf("first block with session: receipt %+v err %v", first, err)
	}
	clock.Advance(time.Hour)

	second, err := h.ackReceiptWithSession(context.Background(), in, "session-second")
	if err != nil {
		t.Fatalf("repeat block: %v", err)
	}
	if !second.OK || second.Blocked || second.Decision != DecisionBlockedNotMine {
		t.Errorf("repeat block receipt = %+v, want ok without a new block", second)
	}
	rec, ok := h.blocked.Lookup(w.ID)
	if !ok {
		t.Fatal("block was not recorded")
	}
	if rec.BlockedBy != "session-first" || !rec.Since.Equal(clock.t.Add(-time.Hour)) {
		t.Errorf("repeat block changed the record: %+v", rec)
	}

	got, err := store.Watch(w.ID)
	if err != nil || got.Status != domain.StatusBlocked || got.Verdict != domain.VerdictBlocked || got.Revision != w.Revision+1 {
		t.Errorf("repeat block changed the Watch: %+v err %v", got, err)
	}
	if got.Blocked == nil || !got.Blocked.Since.Equal(clock.t.Add(-time.Hour)) || got.Blocked.BlockedBy != "session-first" {
		t.Errorf("repeat block changed Watch.Blocked: %+v", got.Blocked)
	}
}

// TestAckBlockedEscalationMetric proves a successful block escalates once and
// an idempotent repeat does not increment the counter again. It also checks
// that the fixer counter is exported as a plain total.
func TestAckBlockedEscalationMetric(t *testing.T) {
	metrics := observe.NewMetrics()
	recorder := observe.NewRecorder(nil, metrics)
	observe.SetDefault(recorder)
	defer observe.SetDefault(nil)

	clock := &stepClock{t: time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)}
	w := resultFixture(domain.VerdictRetry)
	o := resultObligation(w, "session-metric")
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
	in := AckInput{WatchID: w.ID, ObligationID: o.ID, Decision: string(DecisionBlockedNotMine)}

	first, err := h.ackReceiptWithSession(context.Background(), in, "session-first")
	if err != nil || !first.Blocked {
		t.Fatalf("first block with session: receipt %+v err %v", first, err)
	}
	assertCounter(t, metrics, observe.CounterBlocksEscalated, 1)

	clock.Advance(time.Hour)
	if _, err := h.ackReceiptWithSession(context.Background(), in, "session-second"); err != nil {
		t.Fatalf("repeat block: %v", err)
	}
	assertCounter(t, metrics, observe.CounterBlocksEscalated, 1)

	// The fixer spawn path lives in the orchestrator; the counter is exported
	// and increments through the shared helper that path will call.
	observe.FixerSpawned(w.ID)
	assertCounter(t, metrics, observe.CounterFixersSpawned, 1)
}

func assertCounter(t *testing.T, metrics *observe.Metrics, name string, want int) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(metrics.JSON(), &got); err != nil {
		t.Fatalf("metrics JSON: %v", err)
	}
	v, ok := got[name].(float64)
	if !ok || int(v) != want {
		t.Errorf("%s = %v, want %d", name, got[name], want)
	}
}

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

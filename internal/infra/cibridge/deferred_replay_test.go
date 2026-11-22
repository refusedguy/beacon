package cibridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// mutableClock lets a test move the wall clock past the breaker open window
// without rebuilding the Bridge between Refresh calls.
type mutableClock struct {
	at time.Time
}

func (c *mutableClock) Now() time.Time { return c.at }

func countPayload(deliveries []adapterDelivery, payload string) int {
	n := 0
	for _, d := range deliveries {
		if d.payload.Verdict == payload {
			n++
		}
	}
	return n
}

// TestActionableWakeDeliveredWithOpenBreaker proves an actionable verdict never
// loses its wake to the circuit breaker. Three failed informational deliveries
// open the breaker; a keep_working wake on the same adapter still goes out.
func TestActionableWakeDeliveredWithOpenBreaker(t *testing.T) {
	ctx := context.Background()
	transport := errors.New("transport down")
	b, waker, w, exec := breakerTestBridge(t, []error{transport, transport, transport})
	base := time.Unix(2000, 0)

	for i := 0; i < 3; i++ {
		breakerWake(b, w, exec, base.Add(time.Duration(i)*time.Second), "open-breaker")
	}
	if got := b.breakerFor("opencode").state; got != breakerOpen {
		t.Fatalf("breaker state = %v, want open", got)
	}
	failures := b.breakerFailures("opencode")

	actionable := w
	actionable.Verdict = domain.VerdictKeepWorking
	b.wakeOne(ctx, actionable, exec, "keep_working: fix the failed check",
		base.Add(3*time.Second), "cycle-actionable", true)

	if got := len(waker.calls); got != 4 {
		t.Fatalf("deliveries = %d, want 4 (3 failures + actionable wake)", got)
	}
	if got := len(b.deferred["opencode"]); got != 0 {
		t.Fatalf("deferred queue = %d, want 0 for an actionable wake", got)
	}
	if got := b.breakerFailures("opencode"); got != failures {
		t.Fatalf("durable breaker failures = %d, want %d (no defer means no new failure)", got, failures)
	}
}

// setupDeferredRestart opens the breaker, parks a wake with a durable marker,
// then builds a fresh Bridge over the same store. The marker survives the
// restart and the fresh Bridge must rebuild its in-memory queue from it.
func setupDeferredRestart(t *testing.T) (*Bridge, *recordingAdapterWaker, *mutableClock, deferredWake, string) {
	t.Helper()
	transport := errors.New("transport down")
	b, _, w, exec := breakerTestBridge(t, []error{transport, transport, transport})
	base := time.Unix(3000, 0)
	for i := 0; i < 3; i++ {
		breakerWake(b, w, exec, base.Add(time.Duration(i)*time.Second), "defer-cycle")
	}
	const payload = "DEFERRED-REPLAY-PAYLOAD"
	b.wakeOne(context.Background(), w, exec, payload, base.Add(3*time.Second), "defer-cycle", true)

	dw := deferredWake{
		watchID: w.ID, revision: w.Revision, subscriberID: exec.ID,
		adapter: "opencode", text: payload, verdict: w.Verdict,
		cycleID: "defer-cycle", stagedAt: base, watch: w, subscriber: exec,
	}
	if _, err := b.store.Obligation(deferredWakeID(dw)); err != nil {
		t.Fatalf("setup: durable marker missing: %v", err)
	}

	clock := &mutableClock{at: base.Add(breakerOpenWait + time.Minute)}
	replayed := &recordingAdapterWaker{}
	restarted := New(b.store, fakeCI{}, clock, time.Second).WithWaker(replayed)
	return restarted, replayed, clock, dw, payload
}

// TestDeferredWakeSurvivesRestartAndReplaysOnRefresh proves the durable marker
// is read at startup and the parked wake is delivered on the first Refresh
// after the breaker grants a permit. No verdict for the Watch is needed.
func TestDeferredWakeSurvivesRestartAndReplaysOnRefresh(t *testing.T) {
	ctx := context.Background()
	restarted, replayed, clock, _, payload := setupDeferredRestart(t)

	if got := len(restarted.deferred["opencode"]); got != 1 {
		t.Fatalf("restored queue = %d, want 1", got)
	}
	restarted.Refresh(ctx)
	if got := countPayload(replayed.deliveries, payload); got != 0 {
		t.Fatalf("deliveries while breaker OPEN = %d, want 0", got)
	}

	clock.at = clock.at.Add(breakerOpenWait + time.Second)
	restarted.Refresh(ctx)
	if got := countPayload(replayed.deliveries, payload); got != 1 {
		t.Fatalf("deliveries after permit = %d, want 1: %+v", got, replayed.deliveries)
	}
}

// TestDeferredReplayDoesNotDuplicate proves a second Refresh and a duplicate
// queue entry cannot deliver the same obligationID twice. The marker is closed
// after the first successful replay, so PendingObligations drops it too.
func TestDeferredReplayDoesNotDuplicate(t *testing.T) {
	ctx := context.Background()
	restarted, replayed, clock, dw, payload := setupDeferredRestart(t)

	// The first Refresh opens the restored breaker at the current clock time.
	restarted.Refresh(ctx)
	clock.at = clock.at.Add(breakerOpenWait + time.Second)

	restarted.Refresh(ctx)
	if got := countPayload(replayed.deliveries, payload); got != 1 {
		t.Fatalf("first replay deliveries = %d, want 1", got)
	}
	o, err := restarted.store.Obligation(deferredWakeID(dw))
	if err != nil {
		t.Fatalf("marker vanished after replay: %v", err)
	}
	if o.DeliveredAt.IsZero() {
		t.Fatal("marker was not closed after successful replay")
	}

	restarted.deferredMu.Lock()
	restarted.deferred[dw.adapter] = append(restarted.deferred[dw.adapter], dw)
	restarted.deferredMu.Unlock()
	restarted.Refresh(ctx)

	if got := countPayload(replayed.deliveries, payload); got != 1 {
		t.Fatalf("deliveries after duplicate replay = %d, want 1: %+v", got, replayed.deliveries)
	}
}

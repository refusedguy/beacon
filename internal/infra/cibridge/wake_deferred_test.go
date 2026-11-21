package cibridge

import (
	"errors"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// TestOpenBreakerDefersWakeAndKeepsFailureCounter proves a wake refused by an
// OPEN breaker leaves a durable obligation and increments the durable failure
// counter instead of disappearing.
func TestOpenBreakerDefersWakeAndKeepsFailureCounter(t *testing.T) {
	transport := errors.New("transport down")
	b, waker, w, exec := breakerTestBridge(t, []error{transport, transport, transport})
	base := time.Unix(600, 0)
	for i := 0; i < 3; i++ {
		breakerWake(b, w, exec, base.Add(time.Duration(i)*time.Second), "cycle-defer")
	}
	breakerWake(b, w, exec, base.Add(3*time.Second), "cycle-defer")

	if got := len(waker.calls); got != 3 {
		t.Fatalf("deliveries while OPEN = %d, want 3", got)
	}
	dw := deferredWake{watchID: w.ID, revision: w.Revision, subscriberID: exec.ID, adapter: "opencode"}
	if _, err := b.store.Obligation(deferredWakeID(dw)); err != nil {
		t.Fatalf("deferred obligation missing: %v", err)
	}
	if got := b.breakerFailures("opencode"); got == 0 {
		t.Fatal("durable failure counter is empty after OPEN skips")
	}
}

// TestBreakerFailuresSurviveRestart proves a fresh bridge over the same store
// opens the breaker from the persisted failure count.
func TestBreakerFailuresSurviveRestart(t *testing.T) {
	transport := errors.New("transport down")
	b, _, w, exec := breakerTestBridge(t, []error{transport, transport, transport})
	base := time.Unix(700, 0)
	for i := 0; i < 3; i++ {
		breakerWake(b, w, exec, base.Add(time.Duration(i)*time.Second), "cycle-restart")
	}

	restarted := New(b.store, fakeCI{}, fixedClock{at: base.Add(time.Minute)}, time.Second)
	s := restarted.breakerFor("opencode")
	if s.state != breakerOpen {
		t.Fatalf("state after restart = %v, want open", s.state)
	}
	if len(s.outcomes) < breakerMinimumCalls {
		t.Fatalf("restored failures = %d, want at least %d", len(s.outcomes), breakerMinimumCalls)
	}
}

// TestDeferredWakeReplaysAfterProbe proves a parked wake for another obligation
// is delivered when the breaker permits traffic again. The probe for the
// current obligation carries the same pointer, so it is not replayed twice.
func TestDeferredWakeReplaysAfterProbe(t *testing.T) {
	transport := errors.New("transport down")
	b, waker, w, exec := breakerTestBridge(t, []error{transport, transport, transport, nil})
	base := time.Unix(800, 0)
	for i := 0; i < 3; i++ {
		breakerWake(b, w, exec, base.Add(time.Duration(i)*time.Second), "cycle-replay")
	}

	other := domain.Subscriber{ID: "ses_other", Role: domain.RoleOrchestrator, Adapter: "opencode"}
	w.Subscribers = append(w.Subscribers, other)
	b.deferWake(deferredWake{
		watchID: w.ID, revision: w.Revision, subscriberID: other.ID,
		adapter: "opencode", text: "replay", verdict: w.Verdict,
		cycleID: "cycle-replay", stagedAt: base, watch: w, subscriber: other,
	})

	openedAt := b.breakerFor("opencode").openedAt
	probeAt := openedAt.Add(breakerOpenWait + time.Second)
	breakerWake(b, w, exec, probeAt, "cycle-replay")
	if got := len(waker.calls); got != 5 {
		t.Fatalf("deliveries after replay = %d, want 5 (3 failures, probe, deferred replay)", got)
	}
}

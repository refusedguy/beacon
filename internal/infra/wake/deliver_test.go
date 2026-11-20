package wake

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock gives the lease window deterministic boundaries.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestManager(clock *fakeClock) *Manager {
	m := NewManager(1000)
	m.Now = clock.Now
	return m
}

// TestClaimRejectsRepeatAtLiveLease covers the first card check: while a lease
// is live, a second claim is rejected and the caller can see who owns it.
func TestClaimRejectsRepeatAtLiveLease(t *testing.T) {
	clock := newFakeClock()
	m := newTestManager(clock)

	first, err := m.Claim("obl-1", "worker-a")
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	if first.Outcome != ClaimGranted {
		t.Fatalf("first claim outcome = %q, want %q", first.Outcome, ClaimGranted)
	}
	if want := clock.Now().Add(time.Second); !first.Lease.ExpiresAt.Equal(want) {
		t.Fatalf("lease expires at %v, want %v", first.Lease.ExpiresAt, want)
	}

	second, err := m.Claim("obl-1", "worker-b")
	if !errors.Is(err, ErrLeaseLive) {
		t.Fatalf("repeat Claim error = %v, want ErrLeaseLive", err)
	}
	if second.Outcome != ClaimConflict {
		t.Fatalf("repeat claim outcome = %q, want %q", second.Outcome, ClaimConflict)
	}
	if second.OwnerWorkerID != "worker-a" {
		t.Fatalf("blocking owner = %q, want worker-a", second.OwnerWorkerID)
	}
	if m.State("obl-1") != StateClaimed {
		t.Fatal("repeat claim changed the obligation state")
	}
}

// TestExpiredLeaseReclaimPublishesVisibleReason covers the acceptance
// criterion: a worker that never responded leaves a visible reason, and the
// next claim recovers the obligation instead of failing silently.
func TestExpiredLeaseReclaimPublishesVisibleReason(t *testing.T) {
	clock := newFakeClock()
	m := newTestManager(clock)

	if _, err := m.Claim("obl-2", "offline-worker"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	clock.Advance(1000 * time.Millisecond)

	reclaimed, err := m.Claim("obl-2", "worker-b")
	if err != nil {
		t.Fatalf("Claim after expiry: %v", err)
	}
	if reclaimed.Outcome != ClaimReclaimed {
		t.Fatalf("claim outcome = %q, want %q", reclaimed.Outcome, ClaimReclaimed)
	}

	reasons := m.Reasons()
	if len(reasons) != 1 {
		t.Fatalf("published reasons = %d, want 1", len(reasons))
	}
	if reasons[0].Kind != ReasonTimeout {
		t.Fatalf("reason kind = %q, want %q", reasons[0].Kind, ReasonTimeout)
	}
	if reasons[0].ObligationID != "obl-2" || reasons[0].WorkerID != "offline-worker" {
		t.Fatalf("reason = %+v, want obligation obl-2 and offline-worker", reasons[0])
	}
	if !strings.Contains(reasons[0].Text, "offline-worker") {
		t.Fatalf("timeout reason text %q does not name the worker", reasons[0].Text)
	}
}

// TestExpiredLeaseReadAsCreated covers the expiry rule itself: observing an
// expired lease returns the obligation to created.
func TestExpiredLeaseReadAsCreated(t *testing.T) {
	clock := newFakeClock()
	m := newTestManager(clock)

	if _, err := m.Claim("obl-2b", "offline-worker"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	clock.Advance(1001 * time.Millisecond)

	if got := m.State("obl-2b"); got != StateCreated {
		t.Fatalf("state after expiry = %q, want %q", got, StateCreated)
	}
	if _, owned := m.LeaseOf("obl-2b"); owned {
		t.Fatal("expired lease still reported as live")
	}
	if _, err := m.Claim("obl-2b", "worker-b"); err != nil {
		t.Fatalf("Claim after expiry: %v", err)
	}
}

// TestReleaseAfterLostResponseAllowsClaimAgain covers the second card check:
// a lost wake response is cleared by release, then a new claim succeeds.
func TestReleaseAfterLostResponseAllowsClaimAgain(t *testing.T) {
	clock := newFakeClock()
	m := newTestManager(clock)

	if _, err := m.Claim("obl-3", "worker-a"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := m.Release("obl-3", "worker-a"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := m.State("obl-3"); got != StateCreated {
		t.Fatalf("state after release = %q, want %q", got, StateCreated)
	}
	if _, owned := m.LeaseOf("obl-3"); owned {
		t.Fatal("lease still present after release")
	}

	next, err := m.Claim("obl-3", "worker-b")
	if err != nil {
		t.Fatalf("Claim after release: %v", err)
	}
	if next.OwnerWorkerID != "worker-b" {
		t.Fatalf("new lease owner = %q, want worker-b", next.OwnerWorkerID)
	}
}

// TestReleaseRejectsForeignWorkerAndMissingLease pins the ownership guard on
// release, which is the 409 case for another workerId.
func TestReleaseRejectsForeignWorkerAndMissingLease(t *testing.T) {
	clock := newFakeClock()
	m := newTestManager(clock)

	if err := m.Release("missing", "worker-a"); !errors.Is(err, ErrNoLease) {
		t.Fatalf("Release on missing obligation = %v, want ErrNoLease", err)
	}
	if _, err := m.Claim("obl-4", "worker-a"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := m.Release("obl-4", "worker-b"); !errors.Is(err, ErrWrongWorker) {
		t.Fatalf("Release by other worker = %v, want ErrWrongWorker", err)
	}
	if got := m.State("obl-4"); got != StateClaimed {
		t.Fatalf("foreign release changed state to %q", got)
	}
}

// TestFailPublishesVisibleReason covers the third card check: fail puts the
// text in front of the user and leaves the obligation claimable again.
func TestFailPublishesVisibleReason(t *testing.T) {
	clock := newFakeClock()
	var seen []Reason
	m := newTestManager(clock)
	m.OnReason = func(r Reason) { seen = append(seen, r) }

	if _, err := m.Claim("obl-5", "worker-a"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	const text = "wake: session did not start, press Try Again"
	if err := m.Fail("obl-5", "worker-a", text); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	if len(seen) != 1 {
		t.Fatalf("sink got %d reasons, want 1", len(seen))
	}
	if seen[0].Kind != ReasonFail || seen[0].Text != text {
		t.Fatalf("sunk reason = %+v, want fail with %q", seen[0], text)
	}
	if seen[0].ObligationID != "obl-5" || seen[0].WorkerID != "worker-a" {
		t.Fatalf("sunk reason = %+v, want obligation and worker ids", seen[0])
	}
	if got := m.Reasons(); len(got) != 1 || got[0] != seen[0] {
		t.Fatalf("history = %+v, want the sunk reason", got)
	}
	if got := m.State("obl-5"); got != StateCreated {
		t.Fatalf("state after fail = %q, want %q", got, StateCreated)
	}
	if _, err := m.Claim("obl-5", "worker-b"); err != nil {
		t.Fatalf("Claim after fail: %v", err)
	}
}

// TestFailRejectsEmptyReasonAndForeignWorker keeps the visible-reason contract
// from being bypassed by an empty or unauthorised fail.
func TestFailRejectsEmptyReasonAndForeignWorker(t *testing.T) {
	clock := newFakeClock()
	m := newTestManager(clock)

	if err := m.Fail("obl-6", "worker-a", ""); !errors.Is(err, ErrEmptyReason) {
		t.Fatalf("Fail with empty text = %v, want ErrEmptyReason", err)
	}
	if _, err := m.Claim("obl-6", "worker-a"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := m.Fail("obl-6", "worker-b", "boom"); !errors.Is(err, ErrWrongWorker) {
		t.Fatalf("Fail by other worker = %v, want ErrWrongWorker", err)
	}
	if got := m.Reasons(); len(got) != 0 {
		t.Fatalf("published %d reasons for rejected fails, want 0", len(got))
	}
}

// TestConcurrentClaimGrantsOneLease checks that the manager serialises claims
// on the same obligation even when callers arrive at once.
func TestConcurrentClaimGrantsOneLease(t *testing.T) {
	m := newTestManager(newFakeClock())

	const callers = 16
	start := make(chan struct{})
	results := make([]ClaimResult, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = m.Claim("obl-7", "worker-"+string(rune('a'+i)))
		}(i)
	}
	close(start)
	wg.Wait()

	granted := 0
	for i := range results {
		if errs[i] == nil {
			granted++
			if results[i].Outcome != ClaimGranted {
				t.Fatalf("successful claim outcome = %q, want %q", results[i].Outcome, ClaimGranted)
			}
			continue
		}
		if !errors.Is(errs[i], ErrLeaseLive) {
			t.Fatalf("losing claim error = %v, want ErrLeaseLive", errs[i])
		}
	}
	if granted != 1 {
		t.Fatalf("granted %d leases, want exactly 1", granted)
	}
}

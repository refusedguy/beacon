package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestAdmitBudgetExhaustedByOtherItem covers budget exhaustion between two
// admits that share one budget. The first item takes the last unit. The second
// item is refused at the pre-slot check and starts nothing.
func TestAdmitBudgetExhaustedByOtherItem(t *testing.T) {
	lock := newAdmissionLock()
	var budget int32 = 1
	guard := admissionGuard{
		BudgetExhausted: func() bool { return atomic.LoadInt32(&budget) <= 0 },
	}

	started := 0
	releaseFirst, reasonFirst := lock.admit(context.Background(), dispatchAction{Card: "A"}, guard)
	if !reasonFirst.Allowed {
		t.Fatalf("first admit refused: %s", reasonFirst.Reason)
	}
	func() {
		defer releaseFirst()
		atomic.AddInt32(&budget, -1) // another item spends the remaining budget
		started++
	}()

	refusals := 0
	releaseSecond, reasonSecond := lock.admit(context.Background(), dispatchAction{Card: "B"}, guard)
	if reasonSecond.Allowed {
		if releaseSecond != nil {
			releaseSecond()
		}
		t.Fatal("second admit allowed with an exhausted budget")
	}
	refusals++
	if releaseSecond != nil {
		t.Fatal("refused admit returned a slot release")
	}
	if reasonSecond.Reason != "budget exhausted" {
		t.Fatalf("refusal reason = %q, want %q", reasonSecond.Reason, "budget exhausted")
	}
	if started != 1 {
		t.Fatalf("started items = %d, want 1 (only the first call)", started)
	}
	if refusals != 1 {
		t.Fatalf("refusals = %d, want 1 (the second call)", refusals)
	}
}

// TestAdmitDenyAppearsDuringSlotWait covers a deny that arrives while an
// action waits for a slot. A parallel action holds the slot. The exclusive
// action passes the pre-slot check, waits, then the deny is set before the slot
// is released. The post-wait check sees the deny and starts nothing.
func TestAdmitDenyAppearsDuringSlotWait(t *testing.T) {
	lock := newAdmissionLock()
	hold := make(chan struct{})
	parallelStarted := make(chan struct{})

	go func() {
		release, err := lock.acquire(context.Background(), dispatchAction{Card: "P", SupportsParallel: true})
		if err != nil {
			t.Errorf("parallel acquire: %v", err)
			return
		}
		close(parallelStarted)
		<-hold
		release()
	}()
	select {
	case <-parallelStarted:
	case <-time.After(time.Second):
		t.Fatal("parallel action did not take the slot")
	}

	var denied int32
	var preCheckOnce sync.Once
	preCheckDone := make(chan struct{})
	guard := admissionGuard{
		Denied: func() bool {
			preCheckOnce.Do(func() { close(preCheckDone) })
			return atomic.LoadInt32(&denied) != 0
		},
	}

	type outcome struct {
		release func()
		reason  admissionReason
	}
	admitDone := make(chan outcome, 1)
	go func() {
		release, reason := lock.admit(context.Background(), dispatchAction{Card: "E"}, guard)
		admitDone <- outcome{release: release, reason: reason}
	}()

	// Wait until the exclusive action has read the guard before the slot wait,
	// then raise the deny while it is still waiting. The slot is released only
	// afterwards, so the post-wait check is the one that observes the deny.
	select {
	case <-preCheckDone:
	case <-time.After(time.Second):
		t.Fatal("exclusive action did not reach the pre-slot check")
	}
	atomic.StoreInt32(&denied, 1)
	close(hold)

	select {
	case got := <-admitDone:
		if got.reason.Allowed {
			if got.release != nil {
				got.release()
			}
			t.Fatal("exclusive action allowed after a deny appeared during the wait")
		}
		if got.release != nil {
			t.Fatal("exclusive action returned a slot release after denial")
		}
		if got.reason.Reason != "start denied" {
			t.Fatalf("refusal reason = %q, want %q", got.reason.Reason, "start denied")
		}
	case <-time.After(time.Second):
		t.Fatal("exclusive action did not leave the admission wait")
	}
}

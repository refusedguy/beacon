package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestLaunchElementCancelledBeforeAdmissionStartsNothing covers the check
// before admission. A parent that is already gone starts no work and returns
// no handle.
func TestLaunchElementCancelledBeforeAdmissionStartsNothing(t *testing.T) {
	lock := newAdmissionLock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var started int32
	h, result := launchElement(ctx, lock, dispatchAction{Card: "A", SupportsParallel: true}, admissionGuard{}, func(context.Context) {
		atomic.AddInt32(&started, 1)
	})
	if h != nil || result.Started {
		t.Fatalf("launch after cancel = handle %v, result %+v, want no handle", h, result)
	}
	if !strings.Contains(result.Reason, "dispatch interrupted") {
		t.Fatalf("reason = %q, want a dispatch interruption", result.Reason)
	}
	if got := atomic.LoadInt32(&started); got != 0 {
		t.Fatalf("started = %d, want 0", got)
	}
}

// TestLaunchElementCancelAfterAdmissionStopsElement covers the card's case:
// cancel after the first element took its slot. The admitted element stops and
// no element launched afterwards starts.
func TestLaunchElementCancelAfterAdmissionStopsElement(t *testing.T) {
	lock := newAdmissionLock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entered := make(chan struct{})
	stopped := make(chan struct{})
	var aborted, startedAfterCancel int32

	first, firstResult := launchElement(ctx, lock, dispatchAction{Card: "A", SupportsParallel: true}, admissionGuard{}, func(child context.Context) {
		close(entered)
		<-child.Done()
		atomic.AddInt32(&aborted, 1)
		close(stopped)
	})
	if first == nil || !firstResult.Started {
		t.Fatalf("first launch = %+v, want started", firstResult)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first element did not enter its body")
	}

	cancel()

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("cancelled parent did not stop the admitted element")
	}
	if err := first.Err(); err != context.Canceled {
		t.Fatalf("element error = %v, want context.Canceled", err)
	}
	select {
	case <-first.Done():
	default:
		t.Fatal("element Done is open after parent cancel")
	}

	second, secondResult := launchElement(ctx, lock, dispatchAction{Card: "B", SupportsParallel: true}, admissionGuard{}, func(context.Context) {
		atomic.AddInt32(&startedAfterCancel, 1)
	})
	if second != nil || secondResult.Started {
		t.Fatalf("second launch after cancel = handle %v, result %+v, want refusal", second, secondResult)
	}
	if got := atomic.LoadInt32(&aborted); got != 1 {
		t.Fatalf("aborted elements = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&startedAfterCancel); got != 0 {
		t.Fatalf("elements started after cancel = %d, want 0", got)
	}
}

// TestLaunchElementCancelAfterSlotGrantStartsNothing covers the check after
// the slot is granted. The guard cancels the parent on its post-wait check, so
// admission allows the element but the launch must still refuse it.
func TestLaunchElementCancelAfterSlotGrantStartsNothing(t *testing.T) {
	lock := newAdmissionLock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var checks int32
	guard := admissionGuard{
		Denied: func() bool {
			if atomic.AddInt32(&checks, 1) == 2 {
				cancel()
			}
			return false
		},
	}
	var started int32
	h, result := launchElement(ctx, lock, dispatchAction{Card: "C"}, guard, func(context.Context) {
		atomic.AddInt32(&started, 1)
	})
	if h != nil || result.Started {
		t.Fatalf("launch after slot grant = handle %v, result %+v, want refusal", h, result)
	}
	if !strings.Contains(result.Reason, "dispatch interrupted") {
		t.Fatalf("reason = %q, want a dispatch interruption", result.Reason)
	}
	if got := atomic.LoadInt32(&started); got != 0 {
		t.Fatalf("started = %d, want 0", got)
	}
}

// TestAbortHandleStopsElementAndIsIdempotent covers the explicit abort path.
func TestAbortHandleStopsElementAndIsIdempotent(t *testing.T) {
	lock := newAdmissionLock()
	stopped := make(chan struct{})
	h, result := launchElement(context.Background(), lock, dispatchAction{Card: "E"}, admissionGuard{}, func(child context.Context) {
		<-child.Done()
		close(stopped)
	})
	if h == nil || !result.Started {
		t.Fatalf("launch = %+v, want started", result)
	}
	h.Abort()
	h.Abort()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Abort did not stop the element")
	}
	if !h.Aborted() {
		t.Fatal("Aborted = false after Abort")
	}
	if err := h.Err(); err != context.Canceled {
		t.Fatalf("element error = %v, want context.Canceled", err)
	}
}

// TestLaunchPlanBindsHandlePerElement covers the binding of a handle to every
// element of a plan step. Each started element gets one handle in plan order,
// and every handle can abort its element.
func TestLaunchPlanBindsHandlePerElement(t *testing.T) {
	lock := newAdmissionLock()
	plan := planWave([]giantWork{
		{Card: "P1", LoC: 10, Files: []string{"p1.go"}},
		{Card: "P2", LoC: 10, Files: []string{"p2.go"}},
	})
	if len(plan.Steps) != 1 {
		t.Fatalf("steps = %+v, want one parallel step", plan.Steps)
	}
	started := make(chan string, len(plan.Steps[0].Cards))
	handles := launchPlan(context.Background(), lock, plan, admissionGuard{}, func(card string, child context.Context) {
		started <- card
		<-child.Done()
	})
	if len(handles) != len(plan.Steps[0].Cards) {
		t.Fatalf("handles = %d, want %d", len(handles), len(plan.Steps[0].Cards))
	}
	got := make(map[string]bool)
	for _, h := range handles {
		if h == nil {
			t.Fatal("nil handle in the plan")
		}
		got[h.card] = true
		h.Abort()
	}
	for _, card := range plan.Steps[0].Cards {
		if !got[card] {
			t.Fatalf("no handle bound to %s", card)
		}
	}
	for range plan.Steps[0].Cards {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("element did not report its start")
		}
	}
}

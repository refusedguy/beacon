package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestAdmissionLockWaitReadyGatesDelayedAction covers the readiness contract.
// The ready action starts while the delayed one is still waiting on its signal;
// the delayed action starts zero times before that signal and once after it.
func TestAdmissionLockWaitReadyGatesDelayedAction(t *testing.T) {
	lock := newAdmissionLock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readySignal := make(chan struct{})
	started := make(chan string, 2)

	actions := []dispatchAction{
		{
			Card:             "READY",
			SupportsParallel: true,
			WaitReady: func(context.Context) error {
				return nil
			},
		},
		{
			Card:             "DELAYED",
			SupportsParallel: true,
			WaitReady: func(ctx context.Context) error {
				select {
				case <-readySignal:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
		},
	}

	var errCount atomic.Int32
	for _, action := range actions {
		action := action
		go func() {
			release, err := lock.acquire(ctx, action)
			if err != nil {
				errCount.Add(1)
				return
			}
			defer release()
			started <- action.Card
		}()
	}

	select {
	case card := <-started:
		if card != "READY" {
			t.Fatalf("first started action = %q, want READY", card)
		}
	case <-time.After(time.Second):
		t.Fatal("ready action did not start")
	}

	select {
	case card := <-started:
		t.Fatalf("action %q started before its readiness signal", card)
	case <-time.After(20 * time.Millisecond):
	}

	close(readySignal)
	select {
	case card := <-started:
		if card != "DELAYED" {
			t.Fatalf("action after readiness = %q, want DELAYED", card)
		}
	case <-time.After(time.Second):
		t.Fatal("delayed action did not start after its readiness signal")
	}

	if n := errCount.Load(); n != 0 {
		t.Fatalf("acquire errors = %d, want 0", n)
	}
}

// TestAdmissionLockWaitReadyCanceledStartsNothing shows that the readiness
// wait is interruptible. A canceled action leaves without holding a slot or
// running its body.
func TestAdmissionLockWaitReadyCanceledStartsNothing(t *testing.T) {
	lock := newAdmissionLock()
	ctx, cancel := context.WithCancel(context.Background())
	var started atomic.Int32

	readyEntered := make(chan struct{})
	action := dispatchAction{
		Card:             "DELAYED",
		SupportsParallel: true,
		WaitReady: func(ctx context.Context) error {
			close(readyEntered)
			<-ctx.Done()
			return ctx.Err()
		},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		release, err := lock.acquire(ctx, action)
		if err != nil {
			return
		}
		defer release()
		started.Add(1)
	}()

	// Cancel only after the readiness wait has started. The signal proves the
	// action reached WaitReady, so the test no longer guesses at a delay.
	select {
	case <-readyEntered:
	case <-time.After(time.Second):
		t.Fatal("readiness wait did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled readiness wait did not return")
	}
	if n := started.Load(); n != 0 {
		t.Fatalf("started actions = %d, want 0 after cancellation", n)
	}
}

// TestAdmissionLockNilWaitReadyStartsImmediately keeps the no-readiness path
// unchanged: an action without a readiness function enters its slot at once.
func TestAdmissionLockNilWaitReadyStartsImmediately(t *testing.T) {
	lock := newAdmissionLock()
	started := make(chan struct{})
	go func() {
		release, err := lock.acquire(context.Background(), dispatchAction{Card: "PLAIN"})
		if err != nil {
			return
		}
		defer release()
		close(started)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("action without a readiness function did not start")
	}
}

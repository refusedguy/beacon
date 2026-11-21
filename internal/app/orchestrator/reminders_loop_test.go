package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRunNotifyLoopTicksAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	calls := 0
	finished := make(chan struct{})
	go func() {
		RunNotifyLoop(ctx, func(context.Context) error {
			mu.Lock()
			calls++
			mu.Unlock()
			return nil
		}, time.Millisecond)
		close(finished)
	}()

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		got := calls
		mu.Unlock()
		if got >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("notify calls = %d, want at least 2", got)
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("loop did not stop after cancel")
	}
}

func TestRunNotifyLoopNilNotifyReturns(t *testing.T) {
	done := make(chan struct{})
	go func() {
		RunNotifyLoop(context.Background(), nil, time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("nil notify did not return at once")
	}
}

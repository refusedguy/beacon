package orchestrator

import (
	"context"
	"testing"
	"time"
)

func TestRunNotifyLoopTicksAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	calls := make(chan struct{}, 2)
	ticks := make(chan time.Time, 2)
	finished := make(chan struct{})
	go func() {
		runNotifyLoop(ctx, func(context.Context) error {
			calls <- struct{}{}
			return nil
		}, ticks)
		close(finished)
	}()

	ticks <- time.Time{}
	ticks <- time.Time{}
	for i := 0; i < 2; i++ {
		select {
		case <-calls:
		case <-ctx.Done():
			t.Fatal("notify was not called for both ticks")
		}
	}

	cancel()
	waitCtx, waitCancel := context.WithTimeout(t.Context(), time.Second)
	defer waitCancel()
	select {
	case <-finished:
	case <-waitCtx.Done():
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

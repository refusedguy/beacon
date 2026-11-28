package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// waitStore answers Get with one state per call, holding the last state once
// the sequence is exhausted. It lets a test drive a run through a transition
// without a clock or a database.
type waitStore struct {
	states  []core.RunState
	missing bool
	err     error
	calls   int
}

func (s *waitStore) Get(_ context.Context, id core.RunID) (*core.Run, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.calls++
	index := s.calls - 1
	if index >= len(s.states) {
		index = len(s.states) - 1
	}
	if index < 0 || s.missing {
		return nil, nil
	}
	run := core.NewRun(id, "T-wait", time.Now().UTC())
	run.State = s.states[index]
	return run, nil
}

// TestWaitServiceReturnsWaitingCITransition proves WaitFor polls past a running
// snapshot and returns only when the run reaches waiting_ci. A run that stayed
// on its first observed state would fail this assertion.
func TestWaitServiceReturnsWaitingCITransition(t *testing.T) {
	store := &waitStore{states: []core.RunState{
		core.RunRunning, core.RunRunning, core.RunWaitingCI,
	}}
	svc := WaitService{Store: store, Interval: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	run, err := svc.WaitFor(ctx, "run-wait")
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if run.State != core.RunWaitingCI {
		t.Fatalf("state = %q, want waiting_ci", run.State)
	}
	if store.calls < 3 {
		t.Fatalf("Get calls = %d, want the run to be polled through the transition", store.calls)
	}
}

func TestWaitServiceReturnsTerminalRunImmediately(t *testing.T) {
	store := &waitStore{states: []core.RunState{core.RunMerged}}
	svc := WaitService{Store: store, Interval: time.Hour}

	run, err := svc.WaitFor(context.Background(), "run-done")
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if run.State != core.RunMerged || store.calls != 1 {
		t.Fatalf("run = %+v, calls = %d, want merged after one Get", run, store.calls)
	}
}

func TestWaitServiceHonoursDeadline(t *testing.T) {
	store := &waitStore{states: []core.RunState{core.RunRunning}}
	svc := WaitService{Store: store, Interval: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	run, err := svc.WaitFor(ctx, "run-stuck")
	if run != nil {
		t.Fatalf("run = %+v, want nil after the deadline", run)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if store.calls < 2 {
		t.Fatalf("Get calls = %d, want at least one poll before the deadline", store.calls)
	}
}

func TestWaitServiceReportsMissingAndStoreErrors(t *testing.T) {
	t.Run("unwired", func(t *testing.T) {
		run, err := (WaitService{}).WaitFor(context.Background(), "run-unwired")
		if run != nil {
			t.Fatalf("run = %+v, want nil", run)
		}
		if core.CodeOf(err) != core.CodeUnavailable {
			t.Fatalf("error = %v, want code %s", err, core.CodeUnavailable)
		}
	})
	t.Run("missing", func(t *testing.T) {
		svc := WaitService{Store: &waitStore{missing: true}, Interval: time.Millisecond}
		run, err := svc.WaitFor(context.Background(), "run-missing")
		if run != nil {
			t.Fatalf("run = %+v, want nil", run)
		}
		if core.CodeOf(err) != core.CodeNotFound {
			t.Fatalf("error = %v, want code %s", err, core.CodeNotFound)
		}
	})
	t.Run("store error", func(t *testing.T) {
		want := errors.New("store unavailable")
		svc := WaitService{Store: &waitStore{err: want}, Interval: time.Millisecond}
		run, err := svc.WaitFor(context.Background(), "run-error")
		if run != nil {
			t.Fatalf("run = %+v, want nil", run)
		}
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	})
}

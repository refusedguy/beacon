package worker

import (
	"context"
	"errors"
	"testing"
)

type recordingRunner struct {
	calls   []Effect
	errByID map[string]error
}

func (r *recordingRunner) Run(_ context.Context, effect Effect) error {
	r.calls = append(r.calls, effect)
	return r.errByID[effect.ID]
}

func TestDrainRunsEffectOnce(t *testing.T) {
	runner := &recordingRunner{}
	w := New(runner)
	in := Effect{ID: "eff-1", Kind: "notify", Payload: []byte("hello")}

	report, err := w.Drain(context.Background(), []Effect{in})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if report.Executed != 1 || report.Skipped != 0 || len(report.Failures) != 0 {
		t.Fatalf("report = %+v, want one execution", report)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}
	got := runner.calls[0]
	if got.ID != in.ID || got.Kind != in.Kind || string(got.Payload) != string(in.Payload) {
		t.Fatalf("runner got ID=%q Kind=%q Payload=%q, want %+v", got.ID, got.Kind, got.Payload, in)
	}
}

func TestDrainRetryDoesNotRepeatWork(t *testing.T) {
	runner := &recordingRunner{}
	w := New(runner)
	in := Effect{ID: "eff-1", Kind: "notify"}
	next := Effect{ID: "eff-2", Kind: "cleanup"}

	first, err := w.Drain(context.Background(), []Effect{in})
	if err != nil {
		t.Fatalf("first Drain: %v", err)
	}
	if first.Executed != 1 {
		t.Fatalf("first report = %+v, want one execution", first)
	}

	// A replay of the committed outbox after a restart must not redo eff-1,
	// while new work in the same pass still runs.
	again, err := w.Drain(context.Background(), []Effect{in, next})
	if err != nil {
		t.Fatalf("second Drain: %v", err)
	}
	if again.Executed != 1 || again.Skipped != 1 {
		t.Fatalf("second report = %+v, want one execution and one skip", again)
	}
	if len(runner.calls) != 2 || runner.calls[0].ID != "eff-1" || runner.calls[1].ID != "eff-2" {
		t.Fatalf("runner calls = %+v, want eff-1 then eff-2 once", runner.calls)
	}
}

func TestDrainFailureDoesNotRollbackCommit(t *testing.T) {
	boom := errors.New("effect failed")
	runner := &recordingRunner{errByID: map[string]error{"eff-1": boom}}
	w := New(runner)
	in := Effect{ID: "eff-1", Kind: "notify"}

	failed, err := w.Drain(context.Background(), []Effect{in})
	if err != nil {
		t.Fatalf("Drain returned a function error for a runner failure: %v", err)
	}
	if failed.Executed != 0 || len(failed.Failures) != 1 {
		t.Fatalf("report = %+v, want one failure", failed)
	}
	f := failed.Failures[0]
	if f.ID != in.ID || !errors.Is(f.Err, boom) {
		t.Fatalf("failure = %+v, want ID=%q and error %v", f, in.ID, boom)
	}
	if w.Completed(in.ID) {
		t.Fatal("failed effect marked completed")
	}

	// The effect stays eligible for retry. Once it succeeds, a replay is a no-op.
	runner.errByID = nil
	retried, err := w.Drain(context.Background(), []Effect{in})
	if err != nil {
		t.Fatalf("retry Drain: %v", err)
	}
	if retried.Executed != 1 {
		t.Fatalf("retry report = %+v, want one execution", retried)
	}
	replay, err := w.Drain(context.Background(), []Effect{in})
	if err != nil {
		t.Fatalf("replay Drain: %v", err)
	}
	if replay.Skipped != 1 || replay.Executed != 0 {
		t.Fatalf("replay report = %+v, want one skip", replay)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %d, want 2 (one failure, one retry)", len(runner.calls))
	}
}

func TestDrainEmptyOutbox(t *testing.T) {
	runner := &recordingRunner{}
	w := New(runner)
	for _, effects := range [][]Effect{nil, []Effect{}} {
		report, err := w.Drain(context.Background(), effects)
		if err != nil {
			t.Fatalf("Drain: %v", err)
		}
		if report.Executed != 0 || report.Skipped != 0 || len(report.Failures) != 0 {
			t.Fatalf("report = %+v, want zero", report)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %d, want 0", len(runner.calls))
	}
}

func TestDrainDuplicateIDInOnePass(t *testing.T) {
	runner := &recordingRunner{}
	w := New(runner)
	in := Effect{ID: "eff-1", Kind: "notify"}

	report, err := w.Drain(context.Background(), []Effect{in, in})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if report.Executed != 1 || report.Skipped != 1 {
		t.Fatalf("report = %+v, want one execution and one skip", report)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}
}

func TestDrainContinuesAfterFailure(t *testing.T) {
	boom := errors.New("effect failed")
	runner := &recordingRunner{errByID: map[string]error{"bad": boom}}
	w := New(runner)

	report, err := w.Drain(context.Background(), []Effect{
		{ID: "bad", Kind: "notify"},
		{ID: "good", Kind: "notify"},
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if report.Executed != 1 || len(report.Failures) != 1 {
		t.Fatalf("report = %+v, want one execution and one failure", report)
	}
	if len(runner.calls) != 2 || runner.calls[1].ID != "good" {
		t.Fatalf("runner calls = %+v, want bad then good", runner.calls)
	}
}

func TestDrainRejectsNilRunner(t *testing.T) {
	if _, err := New(nil).Drain(context.Background(), nil); !errors.Is(err, ErrNilRunner) {
		t.Fatalf("error = %v, want %v", err, ErrNilRunner)
	}
	var w *EffectWorker
	if _, err := w.Drain(context.Background(), nil); !errors.Is(err, ErrNilRunner) {
		t.Fatalf("nil worker error = %v, want %v", err, ErrNilRunner)
	}
}

func TestDrainRecordsEmptyIDAsFailure(t *testing.T) {
	runner := &recordingRunner{}
	w := New(runner)

	report, err := w.Drain(context.Background(), []Effect{{ID: "  ", Kind: "notify"}})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(report.Failures) != 1 || !errors.Is(report.Failures[0].Err, ErrEmptyEffectID) {
		t.Fatalf("report = %+v, want one empty-id failure", report)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %d, want 0 for an invalid entry", len(runner.calls))
	}
}

func TestEffectRunnerFunc(t *testing.T) {
	called := false
	var runner EffectRunner = EffectRunnerFunc(func(_ context.Context, e Effect) error {
		called = e.ID == "eff-1"
		return nil
	})
	if err := runner.Run(context.Background(), Effect{ID: "eff-1"}); err != nil || !called {
		t.Fatalf("Run: called=%v err=%v", called, err)
	}
}

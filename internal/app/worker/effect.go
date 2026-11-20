// Package worker drains committed outbox entries through an EffectRunner. The
// logic here owns no clock, store or network client. The caller passes entries
// that are already committed, and the runner performs each side effect. A
// runner error stays in the report, so it can never undo the commit that
// queued the effect.
package worker

import (
	"context"
	"errors"
	"strings"
)

// Effect is one committed outbox entry. ID is the effect's idempotency key. It
// stays the same across retries and across process restarts. Kind and Payload
// are opaque to the worker; the runner interprets them.
type Effect struct {
	ID      string
	Kind    string
	Payload []byte
}

// EffectRunner performs the side effect for one committed entry. The worker
// calls it at most once per ID in its lifetime and never for an ID it has
// already completed. An implementation must still be safe to call twice with
// the same ID, because a restart loses the worker's in-memory ledger and can
// replay an entry whose completion was never recorded. The runner owns that
// idempotency at its own boundary.
//
// A runner error leaves the commit intact. The worker reports the error and
// keeps the effect eligible for a later retry.
type EffectRunner interface {
	Run(ctx context.Context, effect Effect) error
}

// EffectRunnerFunc adapts a function to EffectRunner.
type EffectRunnerFunc func(ctx context.Context, effect Effect) error

// Run calls f with effect.
func (f EffectRunnerFunc) Run(ctx context.Context, effect Effect) error {
	return f(ctx, effect)
}

// EffectFailure records one effect that the runner could not complete.
type EffectFailure struct {
	ID  string
	Err error
}

// DrainReport is the outcome of one Drain pass. Failures hold the effects to
// retry. The commit that queued them still stands.
type DrainReport struct {
	Executed int
	Skipped  int
	Failures []EffectFailure
}

var (
	ErrNilRunner     = errors.New("worker: nil effect runner")
	ErrEmptyEffectID = errors.New("worker: empty effect id")
)

// EffectWorker drains committed entries through one runner. It records the IDs
// it completed so a replay never runs the same work twice. Do not share one
// EffectWorker across goroutines; give each draining loop its own.
type EffectWorker struct {
	runner   EffectRunner
	executed map[string]struct{}
}

// New builds an EffectWorker over runner.
func New(runner EffectRunner) *EffectWorker {
	return &EffectWorker{runner: runner, executed: make(map[string]struct{})}
}

// Drain runs every effect that has not completed in this worker. It visits
// entries in the order given. It counts a completed ID as skipped and does not
// send it to the runner again. A failed effect stays eligible for retry, and
// its error goes into the report rather than the function error. No commit is
// rolled back here.
//
// Drain returns ErrNilRunner only for a nil worker or a nil runner. It returns
// no error for a runner failure, an empty ID or an empty batch.
func (w *EffectWorker) Drain(ctx context.Context, effects []Effect) (DrainReport, error) {
	if w == nil || w.runner == nil {
		return DrainReport{}, ErrNilRunner
	}
	var report DrainReport
	for _, effect := range effects {
		if strings.TrimSpace(effect.ID) == "" {
			report.Failures = append(report.Failures, EffectFailure{Err: ErrEmptyEffectID})
			continue
		}
		if _, done := w.executed[effect.ID]; done {
			report.Skipped++
			continue
		}
		if err := w.runner.Run(ctx, effect); err != nil {
			report.Failures = append(report.Failures, EffectFailure{ID: effect.ID, Err: err})
			continue
		}
		w.executed[effect.ID] = struct{}{}
		report.Executed++
	}
	return report, nil
}

// Completed reports whether the effect with id has completed in this worker.
func (w *EffectWorker) Completed(id string) bool {
	if w == nil {
		return false
	}
	_, ok := w.executed[id]
	return ok
}

// Package worker owns retirement of a worker effect whose provider was lost.
//
// PlanRetire is a pure decision over a RetireState snapshot. Retirer applies
// that decision through the narrow RetirePorts port. The package owns no
// database, clock, process or network, so ordering and idempotency are
// testable with fakes. A repeated retire is a no-op.
package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// RetireStatus is the lifecycle status of the effect a worker drives.
type RetireStatus string

const (
	RetireStatusPending    RetireStatus = "pending"
	RetireStatusInProgress RetireStatus = "in_progress"
	RetireStatusComplete   RetireStatus = "complete"
	RetireStatusFailed     RetireStatus = "failed"
	RetireStatusRetired    RetireStatus = "retired"
)

// RetireReasonLost is the reason the retirement event carries when the
// provider disappeared before the worker finished its effect.
const RetireReasonLost = "lost_provider"

// RetireState is the snapshot retire reads. Retire returns a new value and
// never mutates the one it was given.
type RetireState struct {
	EffectKey     string
	WorkerID      string
	LeaseHeld     bool
	HeartbeatOpen bool
	Status        RetireStatus
	Retired       bool
}

// Empty reports whether the snapshot holds a worker to retire. The zero value
// is the empty state, which needs no cleanup and calls no port.
func (s RetireState) Empty() bool {
	return strings.TrimSpace(s.EffectKey) == "" && strings.TrimSpace(s.WorkerID) == ""
}

// RetirePlan is the set of cleanup actions PlanRetire computed.
type RetirePlan struct {
	ReleaseLease   bool
	CloseHeartbeat bool
	MarkComplete   bool
	PublishEvent   bool
}

// PlanRetire computes the cleanup actions for state. The second return value
// is false when retire has nothing to do. That happens for an empty snapshot
// and for one already retired. The function reads no clock and touches no I/O.
func PlanRetire(state RetireState) (RetirePlan, bool) {
	if state.Empty() || state.Retired || state.Status == RetireStatusRetired {
		return RetirePlan{}, false
	}
	return RetirePlan{
		ReleaseLease:   state.LeaseHeld,
		CloseHeartbeat: state.HeartbeatOpen,
		MarkComplete:   true,
		PublishEvent:   true,
	}, true
}

// RetireEvent is the durable record that a worker was retired. The event key
// is the effect key, so a repeat cannot publish a second event.
type RetireEvent struct {
	EffectKey string
	WorkerID  string
	Reason    string
}

// RetirePorts is the I/O boundary of a retire. Retirer calls these operations
// and owns none of them. Adapters own the store, the heartbeat and the event
// sink. Each operation is idempotent by effect key, so a retry after a partial
// failure converges to one effect and one event.
type RetirePorts interface {
	// ReleaseLease drops the worker's lease. released is false when no live
	// lease existed at the time of the call.
	ReleaseLease(ctx context.Context, effectKey, workerID string) (released bool, err error)
	// CloseHeartbeat stops the worker's heartbeat. Closing an already closed
	// heartbeat is a no-op.
	CloseHeartbeat(ctx context.Context, effectKey, workerID string) error
	// MarkEffectComplete marks the effect finished. completed is false when the
	// effect was already terminal, so a repeat applies no second effect.
	MarkEffectComplete(ctx context.Context, effectKey string) (completed bool, err error)
	// PublishRetire writes the retirement event once per effect key.
	PublishRetire(ctx context.Context, event RetireEvent) error
}

// RetireResult reports what one retire did. State is the snapshot to persist.
// Its retired flag makes the next retire a no-op.
type RetireResult struct {
	State           RetireState
	Noop            bool
	LeaseReleased   bool
	HeartbeatClosed bool
	EffectCompleted bool
	EventPublished  bool
}

// ErrNilRetirePorts is reported when a retire with work to do has no ports.
var ErrNilRetirePorts = errors.New("worker: retire: nil ports")

// Retirer applies a retirement through RetirePorts.
type Retirer struct {
	ports RetirePorts
}

// NewRetirer builds a Retirer. A nil port value is allowed because a no-op
// retire needs none. Retire reports the missing port when it needs to act.
func NewRetirer(ports RetirePorts) *Retirer {
	return &Retirer{ports: ports}
}

// Retire cleans up a lost worker. It releases the lease, closes the heartbeat,
// marks the effect complete and publishes the retirement event, in that order.
// A port error stops the sequence and leaves the input state un-retired, so a
// retry starts from the same snapshot. The caller persists the returned state
// to make the next retire a no-op.
func (r *Retirer) Retire(ctx context.Context, in RetireState) (RetireResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	out := RetireResult{State: in}

	plan, ok := PlanRetire(in)
	if !ok {
		out.Noop = true
		return out, nil
	}
	if r == nil || r.ports == nil {
		return out, ErrNilRetirePorts
	}

	if plan.ReleaseLease {
		released, err := r.ports.ReleaseLease(ctx, in.EffectKey, in.WorkerID)
		if err != nil {
			return out, fmt.Errorf("worker: retire: release lease: %w", err)
		}
		out.LeaseReleased = released
	}
	if plan.CloseHeartbeat {
		if err := r.ports.CloseHeartbeat(ctx, in.EffectKey, in.WorkerID); err != nil {
			return out, fmt.Errorf("worker: retire: close heartbeat: %w", err)
		}
		out.HeartbeatClosed = true
	}
	if plan.MarkComplete {
		completed, err := r.ports.MarkEffectComplete(ctx, in.EffectKey)
		if err != nil {
			return out, fmt.Errorf("worker: retire: mark effect complete: %w", err)
		}
		out.EffectCompleted = completed
	}
	if plan.PublishEvent {
		if err := r.ports.PublishRetire(ctx, RetireEvent{
			EffectKey: in.EffectKey,
			WorkerID:  in.WorkerID,
			Reason:    RetireReasonLost,
		}); err != nil {
			return out, fmt.Errorf("worker: retire: publish event: %w", err)
		}
		out.EventPublished = true
	}

	out.State.Status = RetireStatusRetired
	out.State.Retired = true
	out.State.LeaseHeld = false
	out.State.HeartbeatOpen = false
	return out, nil
}

// Package wake manages the lease on a Beacon obligation. A claim reserves an
// obligation for one worker for wakeTimeoutMs. A second claim at a live lease
// is rejected, an expired lease returns the obligation to created, release
// frees a lease after a lost response, and fail publishes a reason the user
// can see. No outcome is left silent.
package wake

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// State is the lifecycle of one obligation in the lease manager.
type State string

const (
	// StateCreated means the obligation is available for claim.
	StateCreated State = "created"
	// StateClaimed means a worker holds a live lease.
	StateClaimed State = "claimed"
)

// ClaimOutcome tells the caller what happened to a claim request.
type ClaimOutcome string

const (
	// ClaimGranted means a fresh lease was created.
	ClaimGranted ClaimOutcome = "granted"
	// ClaimReclaimed means an expired lease was recovered first.
	ClaimReclaimed ClaimOutcome = "reclaimed"
	// ClaimConflict means a live lease blocked the request.
	ClaimConflict ClaimOutcome = "conflict"
)

// ReasonKind separates a worker failure from a lease that ran out of time.
type ReasonKind string

const (
	// ReasonFail is a failure reported by the worker through Fail.
	ReasonFail ReasonKind = "fail"
	// ReasonTimeout is a worker that never responded inside the lease window.
	ReasonTimeout ReasonKind = "timeout"
)

// Reason is the user-visible explanation of a failed or expired obligation.
// Text is meant to be shown verbatim in the host harness, for example a
// "Try Again" notice in Slack.
type Reason struct {
	Kind         ReasonKind
	ObligationID string
	WorkerID     string
	Text         string
}

// Lease is the reservation a worker holds until ExpiresAt.
type Lease struct {
	WorkerID  string
	ClaimedAt time.Time
	ExpiresAt time.Time
}

// Live reports whether the lease still holds at time t.
func (l Lease) Live(t time.Time) bool {
	return l.WorkerID != "" && t.Before(l.ExpiresAt)
}

var (
	// ErrLeaseLive is returned when a live lease blocks a claim.
	ErrLeaseLive = errors.New("wake: obligation has a live lease")
	// ErrNoLease is returned when release or fail finds no live lease.
	ErrNoLease = errors.New("wake: obligation has no live lease")
	// ErrWrongWorker is returned when the caller does not own the lease.
	ErrWrongWorker = errors.New("wake: obligation is leased to another worker")
	// ErrEmptyReason is returned when fail carries no visible text.
	ErrEmptyReason = errors.New("wake: fail reason is empty")
	// ErrInvalidTimeout is returned when wakeTimeoutMs is not positive.
	ErrInvalidTimeout = errors.New("wake: wakeTimeoutMs must be positive")
	// ErrMissingID is returned when an obligation or worker id is empty.
	ErrMissingID = errors.New("wake: obligation id and worker id are required")
)

// ClaimResult reports the outcome of Claim. On a conflict, Lease and
// OwnerWorkerID describe the lease that blocked the request, so a caller that
// lost a response can recognise its own reservation and avoid a second turn.
type ClaimResult struct {
	Outcome       ClaimOutcome
	Lease         Lease
	OwnerWorkerID string
}

// record is the state kept for one obligation. It holds a lease only while the
// obligation is claimed.
type record struct {
	state      State
	lease      Lease
	lastReason string
}

// Manager owns the leases for a set of obligations. It is safe for concurrent
// use. Now and OnReason are optional seams for tests and for wiring the
// published reason into the host harness.
type Manager struct {
	WakeTimeoutMs int64
	Now           func() time.Time
	OnReason      func(Reason)

	mu      sync.Mutex
	records map[string]*record
	reasons []Reason
}

// NewManager builds a lease manager with the given wake timeout in milliseconds.
func NewManager(wakeTimeoutMs int64) *Manager {
	return &Manager{
		WakeTimeoutMs: wakeTimeoutMs,
		records:       make(map[string]*record),
	}
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) recordLocked(id string) *record {
	r := m.records[id]
	if r == nil {
		r = &record{state: StateCreated}
		m.records[id] = r
	}
	return r
}

// publishLocked records a reason and hands it to the sink. The sink must not
// call back into Manager while it runs.
func (m *Manager) publishLocked(reason Reason) {
	m.reasons = append(m.reasons, reason)
	if m.OnReason != nil {
		m.OnReason(reason)
	}
}

// expireLocked returns a claimed obligation to created once its lease window
// is over. It publishes a timeout reason so the worker that never came up is
// visible to the user instead of failing silently. It reports whether it
// expired a lease.
func (m *Manager) expireLocked(id string, r *record, at time.Time) bool {
	if r.state != StateClaimed || r.lease.WorkerID == "" || at.Before(r.lease.ExpiresAt) {
		return false
	}
	worker := r.lease.WorkerID
	r.state = StateCreated
	r.lease = Lease{}
	m.publishLocked(Reason{
		Kind:         ReasonTimeout,
		ObligationID: id,
		WorkerID:     worker,
		Text: fmt.Sprintf(
			"wake: worker %q did not respond within %d ms, obligation %s is available again",
			worker, m.WakeTimeoutMs, id,
		),
	})
	return true
}

// Claim reserves an obligation for workerID. A live lease rejects the claim
// with ErrLeaseLive. An expired lease is recovered first, published as a
// timeout, and then granted to the new worker.
func (m *Manager) Claim(id, workerID string) (ClaimResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if id == "" || workerID == "" {
		return ClaimResult{}, ErrMissingID
	}
	if m.WakeTimeoutMs <= 0 {
		return ClaimResult{}, ErrInvalidTimeout
	}

	at := m.now()
	r := m.recordLocked(id)
	reclaimed := m.expireLocked(id, r, at)
	if r.state == StateClaimed {
		return ClaimResult{
			Outcome:       ClaimConflict,
			Lease:         r.lease,
			OwnerWorkerID: r.lease.WorkerID,
		}, ErrLeaseLive
	}

	outcome := ClaimGranted
	if reclaimed {
		outcome = ClaimReclaimed
	}
	r.state = StateClaimed
	r.lease = Lease{
		WorkerID:  workerID,
		ClaimedAt: at,
		ExpiresAt: at.Add(time.Duration(m.WakeTimeoutMs) * time.Millisecond),
	}
	return ClaimResult{
		Outcome:       outcome,
		Lease:         r.lease,
		OwnerWorkerID: workerID,
	}, nil
}

// Release frees a live lease so another claim can take the obligation. It is
// the path for a lost wake response: the worker that claimed may release
// before trying again. A different worker cannot release someone else's lease.
func (m *Manager) Release(id, workerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	r := m.records[id]
	if r == nil {
		return ErrNoLease
	}
	m.expireLocked(id, r, m.now())
	if r.state != StateClaimed {
		return ErrNoLease
	}
	if r.lease.WorkerID != workerID {
		return ErrWrongWorker
	}
	r.state = StateCreated
	r.lease = Lease{}
	return nil
}

// Fail reports a worker failure with text the user can see. It publishes the
// reason, frees the lease, and leaves the obligation available for a retry.
func (m *Manager) Fail(id, workerID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if text == "" {
		return ErrEmptyReason
	}
	r := m.records[id]
	if r == nil {
		return ErrNoLease
	}
	m.expireLocked(id, r, m.now())
	if r.state != StateClaimed {
		return ErrNoLease
	}
	if r.lease.WorkerID != workerID {
		return ErrWrongWorker
	}
	r.lastReason = text
	r.state = StateCreated
	r.lease = Lease{}
	m.publishLocked(Reason{
		Kind:         ReasonFail,
		ObligationID: id,
		WorkerID:     workerID,
		Text:         text,
	})
	return nil
}

// State returns the effective state of an obligation. Reading the state of an
// expired lease also publishes its timeout reason.
func (m *Manager) State(id string) State {
	m.mu.Lock()
	defer m.mu.Unlock()

	r := m.records[id]
	if r == nil {
		return StateCreated
	}
	m.expireLocked(id, r, m.now())
	return r.state
}

// LeaseOf returns the current lease and whether it is live at the current
// time. It does not expire the lease.
func (m *Manager) LeaseOf(id string) (Lease, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	r := m.records[id]
	if r == nil || r.state != StateClaimed {
		return Lease{}, false
	}
	lease := r.lease
	return lease, lease.Live(m.now())
}

// Reasons returns a copy of every published reason in publication order.
func (m *Manager) Reasons() []Reason {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]Reason(nil), m.reasons...)
}

// Package session holds the durable session registry service. It connects the
// session lifecycle to a store through the SessionStore port. The service owns
// no database handle and speaks no SQL, so the CLI and the tests can supply the
// store they need.
package session

import (
	"errors"
	"strings"
	"time"
)

// Session is one persisted session row. It is a primitive copy of the store
// row, declared here so the app layer names no infra type (DIP, ARDH-15).
type Session struct {
	ID        string
	Status    string
	CreatedAt time.Time
	LastSeen  time.Time
	Metadata  map[string]string
}

// Session status values mirror the persisted values. The adapter in cmd maps
// them to the store constants without changing what is written to disk.
const (
	SessionStatusActive = "active"
	SessionStatusIdle   = "idle"
	SessionStatusClosed = "closed"
)

// DefaultStaleTTL is how old a still-open session must be before
// ReapStaleSessions treats it as stale. Callers can replace it with
// SetStaleTTL.
const DefaultStaleTTL = 24 * time.Hour

// SessionStore is the durable session port. SaveSession records a session,
// GetSession loads one by id and Touch moves its last-seen time. The adapter
// in cmd satisfies this interface. Tests install a mock instead.
type SessionStore interface {
	SaveSession(s Session) error
	GetSession(id string) (Session, bool, error)
	Touch(id string, at time.Time) error
}

var (
	// ErrNilStore means the registry has no store behind it.
	ErrNilStore = errors.New("session: store is nil")
	// ErrEmptyID means a lifecycle call carried no usable session id.
	ErrEmptyID = errors.New("session: empty session id")
	// ErrNoTaskLookup means the store cannot resolve a session by task line.
	// Stores that only implement SessionStore keep working for every other
	// registry method.
	ErrNoTaskLookup = errors.New("session: store has no task lookup")
	// ErrNoSessionCloser means the store cannot mark a session closed.
	ErrNoSessionCloser = errors.New("session: store has no session closer")
	// ErrNoStaleLister means the store cannot list stale sessions.
	ErrNoStaleLister = errors.New("session: store has no stale session lister")
)

// TaskSessionFinder is the optional port a SessionStore can implement so the
// registry can resolve a session by the task it was spawned for. The adapter
// in cmd satisfies it. Stores used by older tests need not implement it.
type TaskSessionFinder interface {
	FindSessionByTask(task string) (Session, bool, error)
}

// SessionCloser is the optional port a SessionStore can implement to mark a
// session closed. Keeping it separate leaves SessionStore usable by stores
// that only save, read and touch sessions.
type SessionCloser interface {
	CloseSession(id string, at time.Time) error
}

// StaleSessionLister is the optional port a SessionStore can implement to
// list still-open sessions last seen before a cutoff, bounded by limit.
type StaleSessionLister interface {
	StaleSessions(before time.Time, limit int) ([]Session, error)
}

// SessionRegistry drives session lifecycle events through a SessionStore. It
// reads the current time through now, so a test can pin the clock. The zero
// value is not ready; use NewSessionRegistry. Do not share one registry across
// goroutines without a lock.
type SessionRegistry struct {
	store    SessionStore
	now      func() time.Time
	staleTTL time.Duration
}

// NewSessionRegistry builds a registry backed by s. A nil store is accepted,
// but every operation then returns ErrNilStore instead of panicking.
func NewSessionRegistry(s SessionStore) *SessionRegistry {
	return &SessionRegistry{store: s, now: time.Now, staleTTL: DefaultStaleTTL}
}

// clock returns the current time from the configured clock, or time.Now on a
// registry built as a zero value.
func (r *SessionRegistry) clock() time.Time {
	if r != nil && r.now != nil {
		return r.now()
	}
	return time.Now()
}

// Start records a new session under id. An empty status becomes active. Both
// CreatedAt and LastSeen get the current time; the store keeps CreatedAt on a
// later save. Metadata is copied so a caller cannot change the saved row by
// editing its map afterwards.
func (r *SessionRegistry) Start(id, status string, metadata map[string]string) error {
	if r == nil || r.store == nil {
		return ErrNilStore
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrEmptyID
	}
	if strings.TrimSpace(status) == "" {
		status = SessionStatusActive
	}
	now := r.clock().UTC()
	return r.store.SaveSession(Session{
		ID:        id,
		Status:    status,
		CreatedAt: now,
		LastSeen:  now,
		Metadata:  copySessionMetadata(metadata),
	})
}

// Resume loads the session with id. found is false when the store has no such
// session. It never creates one.
func (r *SessionRegistry) Resume(id string) (Session, bool, error) {
	if r == nil || r.store == nil {
		return Session{}, false, ErrNilStore
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Session{}, false, ErrEmptyID
	}
	return r.store.GetSession(id)
}

// ResumeByTask loads the session spawned for task. Resume alone needs an
// opaque session id, which a restarted command does not have; task is the
// stable line the caller knows. found is false when the store has no session
// for that task. The registry never creates one here.
func (r *SessionRegistry) ResumeByTask(task string) (Session, bool, error) {
	if r == nil || r.store == nil {
		return Session{}, false, ErrNilStore
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return Session{}, false, ErrEmptyID
	}
	finder, ok := r.store.(TaskSessionFinder)
	if !ok {
		return Session{}, false, ErrNoTaskLookup
	}
	return finder.FindSessionByTask(task)
}

// Heartbeat moves the last-seen time of id to now. A live session writes this
// while it runs; the store leaves the other fields alone.
func (r *SessionRegistry) Heartbeat(id string) error {
	if r == nil || r.store == nil {
		return ErrNilStore
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrEmptyID
	}
	return r.store.Touch(id, r.clock().UTC())
}

// SetStaleTTL replaces the age past which a still-open session counts as
// stale. A non-positive value makes ReapStaleSessions return no rows.
func (r *SessionRegistry) SetStaleTTL(ttl time.Duration) {
	if r != nil {
		r.staleTTL = ttl
	}
}

// CloseSession marks id closed at the registry's current time. It needs a
// store that implements SessionCloser; other stores return
// ErrNoSessionCloser.
func (r *SessionRegistry) CloseSession(id string) error {
	if r == nil || r.store == nil {
		return ErrNilStore
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrEmptyID
	}
	closer, ok := r.store.(SessionCloser)
	if !ok {
		return ErrNoSessionCloser
	}
	return closer.CloseSession(id, r.clock().UTC())
}

// ReapStaleSessions lists still-open sessions last seen before now minus the
// stale TTL, bounded by limit. It needs a store that implements
// StaleSessionLister; other stores return ErrNoStaleLister.
func (r *SessionRegistry) ReapStaleSessions(limit int) ([]Session, error) {
	if r == nil || r.store == nil {
		return nil, ErrNilStore
	}
	lister, ok := r.store.(StaleSessionLister)
	if !ok {
		return nil, ErrNoStaleLister
	}
	if r.staleTTL <= 0 {
		return []Session{}, nil
	}
	before := r.clock().UTC().Add(-r.staleTTL)
	return lister.StaleSessions(before, limit)
}

func copySessionMetadata(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

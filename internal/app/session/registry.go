// Package session holds the durable session registry service. It connects the
// session lifecycle to a store through the SessionStore port. The service owns
// no database handle and speaks no SQL, so the CLI and the tests can supply the
// store they need.
package session

import (
	"errors"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/infra/store"
)

// SessionStore is the durable session port. SaveSession records a session,
// GetSession loads one by id and Touch moves its last-seen time. *store.DB
// satisfies this interface. Tests install a mock instead.
type SessionStore interface {
	SaveSession(s store.Session) error
	GetSession(id string) (store.Session, bool, error)
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
)

// TaskSessionFinder is the optional port a SessionStore can implement so the
// registry can resolve a session by the task it was spawned for. *store.DB
// satisfies it. Stores used by older tests need not implement it.
type TaskSessionFinder interface {
	FindSessionByTask(task string) (store.Session, bool, error)
}

// SessionRegistry drives session lifecycle events through a SessionStore. It
// reads the current time through now, so a test can pin the clock. The zero
// value is not ready; use NewSessionRegistry. Do not share one registry across
// goroutines without a lock.
type SessionRegistry struct {
	store SessionStore
	now   func() time.Time
}

// NewSessionRegistry builds a registry backed by s. A nil store is accepted,
// but every operation then returns ErrNilStore instead of panicking.
func NewSessionRegistry(s SessionStore) *SessionRegistry {
	return &SessionRegistry{store: s, now: time.Now}
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
		status = store.SessionStatusActive
	}
	now := r.clock().UTC()
	return r.store.SaveSession(store.Session{
		ID:        id,
		Status:    status,
		CreatedAt: now,
		LastSeen:  now,
		Metadata:  copySessionMetadata(metadata),
	})
}

// Resume loads the session with id. found is false when the store has no such
// session. It never creates one.
func (r *SessionRegistry) Resume(id string) (store.Session, bool, error) {
	if r == nil || r.store == nil {
		return store.Session{}, false, ErrNilStore
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return store.Session{}, false, ErrEmptyID
	}
	return r.store.GetSession(id)
}

// ResumeByTask loads the session spawned for task. Resume alone needs an
// opaque session id, which a restarted command does not have; task is the
// stable line the caller knows. found is false when the store has no session
// for that task. The registry never creates one here.
func (r *SessionRegistry) ResumeByTask(task string) (store.Session, bool, error) {
	if r == nil || r.store == nil {
		return store.Session{}, false, ErrNilStore
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return store.Session{}, false, ErrEmptyID
	}
	finder, ok := r.store.(TaskSessionFinder)
	if !ok {
		return store.Session{}, false, ErrNoTaskLookup
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

func copySessionMetadata(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

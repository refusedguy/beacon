// Package session holds the durable session registry service. It connects the
// session lifecycle to a store through the SessionStore port. The service owns
// no database handle and speaks no SQL, so the CLI and the tests can supply the
// store they need.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
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
// ReapStaleSessions treats it as stale.
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
	// ErrSessionIdentityMismatch means a registry row belongs to a different
	// session than the caller asked for. Resuming that row would open another
	// session than the saved id (CCx-10).
	ErrSessionIdentityMismatch = errors.New("session: session identity mismatch")
	// ErrSessionRevisionChanged means a session record changed between two
	// reads of its registry. A caller must not open a session chosen from a
	// record that moved while it was being read (CC-05).
	ErrSessionRevisionChanged = errors.New("session: revision changed between reads")
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

// ResumeIdentified loads id and checks that the row describes that same
// session. Resume stays best effort for older callers; attach uses this form
// when the saved record carries an identity (CCx-10). A row with a foreign id
// returns ErrSessionIdentityMismatch and is not reported as found.
func (r *SessionRegistry) ResumeIdentified(id string) (Session, bool, error) {
	stored, found, err := r.Resume(id)
	if err != nil || !found {
		return stored, found, err
	}
	if strings.TrimSpace(stored.ID) != id {
		return Session{}, false, fmt.Errorf("%w: requested %q, row %q", ErrSessionIdentityMismatch, id, stored.ID)
	}
	return stored, true, nil
}

// Restore writes a rebuilt session row and returns it as the resumed session.
// A caller uses it after Resume reports a clean miss. The saved attach record
// supplies the id and task line because the state row is gone. Restore copies
// the metadata and fills missing times from the registry clock. A write error
// goes back to the caller, which can keep the saved session id.
func (r *SessionRegistry) Restore(seed Session) (Session, error) {
	if r == nil || r.store == nil {
		return Session{}, ErrNilStore
	}
	seed.ID = strings.TrimSpace(seed.ID)
	if seed.ID == "" {
		return Session{}, ErrEmptyID
	}
	if strings.TrimSpace(seed.Status) == "" {
		seed.Status = SessionStatusActive
	}
	now := r.clock().UTC()
	if seed.CreatedAt.IsZero() {
		seed.CreatedAt = now
	}
	seed.LastSeen = now
	seed.Metadata = copySessionMetadata(seed.Metadata)
	if err := r.store.SaveSession(seed); err != nil {
		return Session{}, err
	}
	return seed, nil
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

// SessionRevision returns a stable signature of a session record. The caller
// passes the record's fields in a fixed order. Equal records give the same
// revision, and a change to any field gives a different one. Callers take the
// revision before and after a read so they can refuse a record that changed in
// between (CC-05).
func SessionRevision(fields ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return hex.EncodeToString(sum[:])
}

// MetadataCompactionCutoff is the metadata key that records the journal index
// of the newest complete compaction. A fork uses it to bound its reverse scan.
const MetadataCompactionCutoff = "compactionCutoff"

// CompactionCutoff reads the recorded fork cutoff. A missing or malformed
// value reports false, so the caller scans the whole journal instead of
// trusting a broken record.
func CompactionCutoff(metadata map[string]string) (int, bool) {
	raw, ok := metadata[MetadataCompactionCutoff]
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// WithCompactionCutoff returns a copy of metadata with index recorded as the
// fork cutoff. The input map stays owned by the caller.
func WithCompactionCutoff(metadata map[string]string, index int) map[string]string {
	out := make(map[string]string, len(metadata)+1)
	for k, v := range metadata {
		out[k] = v
	}
	out[MetadataCompactionCutoff] = strconv.Itoa(index)
	return out
}

// MetadataJournalPath is the metadata key that records the file path of the
// journal a session writes. Resume appends to that file so one session keeps a
// single journal across restarts.
const MetadataJournalPath = "ai.beacon/journalPath"

// MetadataWorkspaceRoots records the workspace roots a session was created
// with. Resume uses it to restore only the roots the caller asks for again
// (CCx-15).
const MetadataWorkspaceRoots = "ai.beacon/workspaceRoots"

// MetadataReadOnlyRoots records the roots that stay read-only. The list is
// copied through resume so an explicit read-only setting from an older record
// survives (CCx-15).
const MetadataReadOnlyRoots = "ai.beacon/readOnlyRoots"

// WorkspaceRoots reads the saved workspace roots. A missing, blank or empty
// value reports false, so an old record counts as having no roots.
func WorkspaceRoots(metadata map[string]string) ([]string, bool) {
	return decodeRoots(metadata[MetadataWorkspaceRoots])
}

// ReadOnlyRoots reads the read-only roots saved with the session.
func ReadOnlyRoots(metadata map[string]string) ([]string, bool) {
	return decodeRoots(metadata[MetadataReadOnlyRoots])
}

func decodeRoots(raw string) ([]string, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, false
	}
	var roots []string
	for _, part := range strings.Split(raw, ",") {
		if root := strings.TrimSpace(part); root != "" {
			roots = append(roots, root)
		}
	}
	return roots, len(roots) > 0
}

func encodeRoots(roots []string) string {
	kept := make([]string, 0, len(roots))
	for _, root := range roots {
		if r := strings.TrimSpace(root); r != "" {
			kept = append(kept, r)
		}
	}
	return strings.Join(kept, ",")
}

// WithWorkspaceRoots returns a copy of metadata with roots recorded as the
// workspace roots. Empty entries are dropped. The input map stays owned by the
// caller.
func WithWorkspaceRoots(metadata map[string]string, roots []string) map[string]string {
	out := copySessionMetadata(metadata)
	encoded := encodeRoots(roots)
	if encoded == "" {
		delete(out, MetadataWorkspaceRoots)
		return out
	}
	out[MetadataWorkspaceRoots] = encoded
	return out
}

// WithReadOnlyRoots returns a copy of metadata with roots recorded as
// read-only. The input map stays owned by the caller.
func WithReadOnlyRoots(metadata map[string]string, roots []string) map[string]string {
	out := copySessionMetadata(metadata)
	encoded := encodeRoots(roots)
	if encoded == "" {
		delete(out, MetadataReadOnlyRoots)
		return out
	}
	out[MetadataReadOnlyRoots] = encoded
	return out
}

// ResumeWorkspaceRoots rebuilds the roots of a resumed session. It keeps only
// roots that were both saved in the record and requested again, so resume
// never adds a root the caller did not ask for. The other keys are copied once,
// which leaves explicit settings from the older record in a single instance.
func ResumeWorkspaceRoots(saved map[string]string, requested []string) map[string]string {
	out := copySessionMetadata(saved)
	savedRoots, ok := WorkspaceRoots(saved)
	if !ok {
		delete(out, MetadataWorkspaceRoots)
		return out
	}
	allowed := make(map[string]bool, len(requested))
	for _, root := range requested {
		if r := strings.TrimSpace(root); r != "" {
			allowed[r] = true
		}
	}
	kept := make([]string, 0, len(savedRoots))
	seen := make(map[string]bool, len(savedRoots))
	for _, root := range savedRoots {
		if allowed[root] && !seen[root] {
			kept = append(kept, root)
			seen[root] = true
		}
	}
	return WithWorkspaceRoots(out, kept)
}

// ResumeWorkspace loads id and restores its workspace roots from the saved
// metadata. Only roots named in requested survive. found is false when the
// store has no such session.
func (r *SessionRegistry) ResumeWorkspace(id string, requested []string) (Session, bool, error) {
	stored, found, err := r.Resume(id)
	if err != nil || !found {
		return stored, found, err
	}
	stored.Metadata = ResumeWorkspaceRoots(stored.Metadata, requested)
	return stored, true, nil
}

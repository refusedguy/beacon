// Package store: capability-based executor registry with availability.
//
// ADR 0100/0199 asks for a registry that keeps capabilities, runtime, auth
// state and a lease, and for refusals to be typed. ADR 0090 keeps registry
// state separate from Watch verdicts: this file never reads or writes a Watch.
//
// The card owns only executors.go, so the table is created on first use, the
// way archive.go and observability.go create theirs. No migration file and no
// change to db.go is needed. The existing agent_registry from registry.go is a
// different table with a different job (delivery address per project); this
// registry answers "who can do this capability right now".
//
// Availability has two inputs. Disabled is the explicit toggle written by
// SetAvailable. LeaseUntil is refreshed by Register and Heartbeat. An executor
// that is disabled, revoked or past its lease stays in List but cannot be
// selected. The returned entry derives Available at read time; writes ignore
// that field and use Disabled.
//
// A child executor carries ParentID. Its capabilities are intersected with the
// parent's at registration through a separate capability check, so a child
// can never widen the rights it was given (ADR 0100/0199).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// createExecutorRegistrySQL owns the capability registry table. AuthState is
// stored as text from the closed AuthState set; capabilities use the same JSON
// encoding as agent_registry so the two registries read alike. Lease durations
// and instants are stored as nanoseconds in SQLite INTEGER columns.
const createExecutorRegistrySQL = `
CREATE TABLE IF NOT EXISTS executor_registry (
  id             TEXT PRIMARY KEY,
  runtime        TEXT NOT NULL DEFAULT '',
  capabilities   BLOB NOT NULL DEFAULT '[]',
  auth_state     TEXT NOT NULL DEFAULT 'authenticated',
  parent_id      TEXT NOT NULL DEFAULT '',
  disabled       INTEGER NOT NULL DEFAULT 0,
  lease_ns       INTEGER NOT NULL DEFAULT 0,
  last_heartbeat INTEGER NOT NULL DEFAULT 0,
  lease_until    INTEGER NOT NULL DEFAULT 0,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS executor_registry_by_parent
  ON executor_registry(parent_id);
`

// AuthState is the authorization state of one executor. It is a separate axis
// from availability: a revoked executor remains visible in List, but a request
// to it is refused with ErrExecutorUnavailable.
type AuthState string

const (
	// AuthStateAuthenticated grants the declared capabilities.
	AuthStateAuthenticated AuthState = "authenticated"
	// AuthStateRevoked withholds the declared capabilities.
	AuthStateRevoked AuthState = "revoked"
)

// Valid reports whether a is one of the two canonical states. The zero value
// fails closed; Register fills it with AuthStateAuthenticated.
func (a AuthState) Valid() bool {
	return a == AuthStateAuthenticated || a == AuthStateRevoked
}

// Typed refusals from the registry. Callers use errors.Is on the sentinel and
// errors.As on the structured error to get the executor id and the reason.
var (
	ErrExecutorUnavailable  = errors.New("store: executor unavailable")
	ErrExecutorNotFound     = errors.New("store: executor not found")
	ErrCapabilityEscalation = errors.New("store: child capability escalation")
)

// ExecutorUnavailableError is the typed refusal for a request to an executor
// that is disabled, revoked or past its lease.
type ExecutorUnavailableError struct {
	ID     string
	Reason string
}

func (e *ExecutorUnavailableError) Error() string {
	if e.ID == "" {
		return fmt.Sprintf("%s: %s", ErrExecutorUnavailable, e.Reason)
	}
	return fmt.Sprintf("%s: executor %q: %s", ErrExecutorUnavailable, e.ID, e.Reason)
}

// Unwrap makes errors.Is(err, ErrExecutorUnavailable) true.
func (e *ExecutorUnavailableError) Unwrap() error { return ErrExecutorUnavailable }

// ExecutorNotFoundError is returned by writes that name an unknown executor.
type ExecutorNotFoundError struct {
	ID string
}

func (e *ExecutorNotFoundError) Error() string {
	return fmt.Sprintf("%s: %q", ErrExecutorNotFound, e.ID)
}

// Unwrap makes errors.Is(err, ErrExecutorNotFound) true.
func (e *ExecutorNotFoundError) Unwrap() error { return ErrExecutorNotFound }

// CapabilityEscalationError names the capability a child tried to add beyond
// its parent's ceiling.
type CapabilityEscalationError struct {
	ParentID   string
	ChildID    string
	Capability domain.Capability
}

func (e *CapabilityEscalationError) Error() string {
	return fmt.Sprintf("%s: child %q of %q requested capability %q not held by parent",
		ErrCapabilityEscalation, e.ChildID, e.ParentID, e.Capability)
}

// Unwrap makes errors.Is(err, ErrCapabilityEscalation) true.
func (e *CapabilityEscalationError) Unwrap() error { return ErrCapabilityEscalation }

// ExecutorEntry is one registered executor. Capabilities are typechecked
// against the closed domain dictionary before they reach the row. Runtime is
// optional and uses domain.Runtime when set. Lease must be positive.
//
// Disabled is the stored toggle written by SetAvailable. Available is derived
// from Disabled, AuthState and LeaseUntil when List or SelectAvailable reads
// the row; callers must not set it on a write.
type ExecutorEntry struct {
	ID            string
	Runtime       domain.Runtime
	Capabilities  []domain.Capability
	AuthState     AuthState
	ParentID      string
	Disabled      bool
	Available     bool
	Lease         time.Duration
	LastHeartbeat time.Time
	LeaseUntil    time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// availableAt reports the derived availability of a stored row. A lease that
// ends exactly at now is already expired.
func (e ExecutorEntry) availableAt(now time.Time) bool {
	return !e.Disabled && e.AuthState == AuthStateAuthenticated && now.Before(e.LeaseUntil)
}

// SelectRequest narrows SelectAvailable. Every field is optional: an empty
// request selects any available executor. ExecutorID targets one executor, so
// a negative answer is a typed refusal for that id. Capabilities are a subset
// test; the executor must hold every requested capability.
type SelectRequest struct {
	ExecutorID   string
	Capabilities []domain.Capability
	Runtime      domain.Runtime
}

// ExecutorRegistry is the capability registry of one DB. Build it with
// NewExecutorRegistry; every method then uses the same store. The now function
// is a field so tests can advance the lease clock without sleeping.
type ExecutorRegistry struct {
	db  *DB
	now func() time.Time
}

// NewExecutorRegistry binds a registry to a database. It refuses a nil DB
// rather than returning a registry that would panic on first use.
func NewExecutorRegistry(db *DB) (*ExecutorRegistry, error) {
	if db == nil {
		return nil, domain.NewAppError(domain.CodeInvalidInput,
			"store: executors: nil database")
	}
	return &ExecutorRegistry{db: db, now: time.Now}, nil
}

// validateExecutorEntry checks the closed dictionaries at the boundary. An
// unknown capability or runtime is an INVALID_INPUT AppError, never a new
// silent name that would slip through capability matching (ADR 0089).
func validateExecutorEntry(e ExecutorEntry) error {
	if strings.TrimSpace(e.ID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: executors: empty executor id")
	}
	if e.Runtime != "" && !e.Runtime.Valid() {
		return domain.NewAppError(domain.CodeInvalidInput,
			fmt.Sprintf("store: executors: unknown runtime %q", e.Runtime))
	}
	if e.AuthState != "" && !e.AuthState.Valid() {
		return domain.NewAppError(domain.CodeInvalidInput,
			fmt.Sprintf("store: executors: unknown auth state %q", e.AuthState))
	}
	for _, c := range e.Capabilities {
		if !c.Valid() {
			return domain.NewAppError(domain.CodeInvalidInput,
				fmt.Sprintf("store: executors: unknown capability %q", c))
		}
	}
	if e.Lease <= 0 {
		return domain.NewAppError(domain.CodeInvalidInput, "store: executors: non-positive lease")
	}
	if e.ParentID != "" && e.ParentID == e.ID {
		return domain.NewAppError(domain.CodeInvalidInput,
			"store: executors: executor cannot be its own parent")
	}
	return nil
}

// hasCapability and hasCapabilities implement subset checks over the closed
// capability dictionary. Duplicate and empty requests are harmless.
func hasCapability(have []domain.Capability, want domain.Capability) bool {
	for _, c := range have {
		if c == want {
			return true
		}
	}
	return false
}

func hasCapabilities(have, need []domain.Capability) bool {
	for _, c := range need {
		if !hasCapability(have, c) {
			return false
		}
	}
	return true
}

func encodeExecutorCapabilities(caps []domain.Capability) []byte {
	values := make([]string, 0, len(caps))
	for _, c := range caps {
		values = append(values, string(c))
	}
	return encodeCapabilities(values)
}

func decodeExecutorCapabilities(raw []byte) []domain.Capability {
	values := decodeCapabilities(raw)
	out := make([]domain.Capability, 0, len(values))
	for _, v := range values {
		out = append(out, domain.Capability(v))
	}
	return out
}

func ensureExecutorTable(tx *sql.Tx) error {
	if _, err := tx.Exec(createExecutorRegistrySQL); err != nil {
		return fmt.Errorf("store: executors: create table: %w", err)
	}
	return nil
}

// Register inserts or refreshes one executor. A repeat with the same id
// refreshes the mutable fields, resets the lease and keeps the original
// created_at. A child registration runs CheckChildCapabilities first, so a
// child cannot claim a capability its parent does not hold.
func (r *ExecutorRegistry) Register(e ExecutorEntry) error {
	if err := validateExecutorEntry(e); err != nil {
		return err
	}
	if e.ParentID != "" {
		if err := r.CheckChildCapabilities(e.ParentID, e.Capabilities); err != nil {
			return err
		}
	}
	if e.AuthState == "" {
		e.AuthState = AuthStateAuthenticated
	}
	now := r.now().UTC()
	e.LastHeartbeat = now
	e.LeaseUntil = now.Add(e.Lease)
	e.UpdatedAt = now
	createdAtNS := now.UnixNano()

	return r.db.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureExecutorTable(tx); err != nil {
			return err
		}
		var found int
		err := tx.QueryRow(`SELECT 1 FROM executor_registry WHERE id = ?`, e.ID).Scan(&found)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: executors: lookup: %w", err)
		}
		if err == nil {
			if err := tx.QueryRow(`SELECT created_at FROM executor_registry WHERE id = ?`, e.ID).Scan(&createdAtNS); err != nil {
				return fmt.Errorf("store: executors: read created_at: %w", err)
			}
		}
		_, err = tx.Exec(`
			INSERT INTO executor_registry
			  (id, runtime, capabilities, auth_state, parent_id, disabled,
			   lease_ns, last_heartbeat, lease_until, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
			  runtime        = excluded.runtime,
			  capabilities   = excluded.capabilities,
			  auth_state     = excluded.auth_state,
			  parent_id      = excluded.parent_id,
			  disabled       = excluded.disabled,
			  lease_ns       = excluded.lease_ns,
			  last_heartbeat = excluded.last_heartbeat,
			  lease_until    = excluded.lease_until,
			  updated_at     = excluded.updated_at`,
			e.ID, string(e.Runtime), encodeExecutorCapabilities(e.Capabilities),
			string(e.AuthState), e.ParentID, boolToInt(e.Disabled),
			int64(e.Lease), now.UnixNano(), e.LeaseUntil.UnixNano(),
			createdAtNS, now.UnixNano())
		if err != nil {
			return fmt.Errorf("store: executors: upsert: %w", err)
		}
		return nil
	})
}

// boolToInt stores the explicit availability toggle as a SQLite integer.
func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// Heartbeat refreshes last_heartbeat and extends the lease by the duration
// stored at registration. A live heartbeat revives an expired lease without
// changing the explicit Disabled toggle.
func (r *ExecutorRegistry) Heartbeat(id string) error {
	if strings.TrimSpace(id) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: executors: empty executor id")
	}
	now := r.now().UTC().UnixNano()
	var missing bool
	err := r.db.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureExecutorTable(tx); err != nil {
			return err
		}
		res, err := tx.Exec(`
			UPDATE executor_registry
			SET last_heartbeat = ?, lease_until = ? + lease_ns, updated_at = ?
			WHERE id = ?`, now, now, now, id)
		if err != nil {
			return fmt.Errorf("store: executors: heartbeat: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: executors: heartbeat rows: %w", err)
		}
		missing = n == 0
		return nil
	})
	if err != nil {
		return err
	}
	if missing {
		return &ExecutorNotFoundError{ID: id}
	}
	return nil
}

// SetAvailable flips the explicit availability toggle. A disabled executor
// stays in List with Available=false and is skipped by SelectAvailable.
func (r *ExecutorRegistry) SetAvailable(id string, available bool) error {
	if strings.TrimSpace(id) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: executors: empty executor id")
	}
	now := r.now().UTC().UnixNano()
	var missing bool
	err := r.db.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureExecutorTable(tx); err != nil {
			return err
		}
		res, err := tx.Exec(`
			UPDATE executor_registry SET disabled = ?, updated_at = ? WHERE id = ?`,
			boolToInt(!available), now, id)
		if err != nil {
			return fmt.Errorf("store: executors: set available: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: executors: set available rows: %w", err)
		}
		missing = n == 0
		return nil
	})
	if err != nil {
		return err
	}
	if missing {
		return &ExecutorNotFoundError{ID: id}
	}
	return nil
}

// scanExecutor reads one row from a query. Auths stored before this registry
// existed are treated as authenticated so an empty value cannot silently
// blacklist an executor.
func scanExecutor(scan func(...any) error) (ExecutorEntry, error) {
	var e ExecutorEntry
	var runtime, authState, parentID string
	var disabled int
	var leaseNS, lastHeartbeat, leaseUntil, createdAt, updatedAt int64
	var caps []byte
	if err := scan(&e.ID, &runtime, &caps, &authState, &parentID, &disabled,
		&leaseNS, &lastHeartbeat, &leaseUntil, &createdAt, &updatedAt); err != nil {
		return ExecutorEntry{}, err
	}
	e.Runtime = domain.Runtime(runtime)
	e.Capabilities = decodeExecutorCapabilities(caps)
	e.AuthState = AuthState(authState)
	if e.AuthState == "" {
		e.AuthState = AuthStateAuthenticated
	}
	e.ParentID = parentID
	e.Disabled = disabled != 0
	e.Lease = time.Duration(leaseNS)
	e.LastHeartbeat = time.Unix(0, lastHeartbeat).UTC()
	e.LeaseUntil = time.Unix(0, leaseUntil).UTC()
	e.CreatedAt = time.Unix(0, createdAt).UTC()
	e.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return e, nil
}

const executorColumns = `id, runtime, capabilities, auth_state, parent_id, disabled,
       lease_ns, last_heartbeat, lease_until, created_at, updated_at`

// getExecutor reads a row by id without applying availability. It is the
// parent lookup used by the capability check: an unavailable parent must still
// constrain its child.
func (r *ExecutorRegistry) getExecutor(id string) (ExecutorEntry, bool, error) {
	ok, err := tableExists(context.Background(), r.db.readDB, "executor_registry")
	if err != nil || !ok {
		return ExecutorEntry{}, false, err
	}
	e, err := scanExecutor(r.db.readDB.QueryRow(`SELECT `+executorColumns+`
		FROM executor_registry WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutorEntry{}, false, nil
	}
	if err != nil {
		return ExecutorEntry{}, false, err
	}
	return e, true, nil
}

// CheckChildCapabilities is the separate capability check from ADR 0100/0199.
// Every child capability must already be in the parent's set; the parent's
// capabilities are never widened by the child. A missing parent and a capability
// the parent does not hold are typed errors.
func (r *ExecutorRegistry) CheckChildCapabilities(parentID string, child []domain.Capability) error {
	parent, found, err := r.getExecutor(parentID)
	if err != nil {
		return err
	}
	if !found {
		return &ExecutorNotFoundError{ID: parentID}
	}
	for _, c := range child {
		if !hasCapability(parent.Capabilities, c) {
			return &CapabilityEscalationError{ParentID: parentID, Capability: c}
		}
	}
	return nil
}

// List returns every registered executor, including disabled, revoked and
// expired ones, ordered by id. Available is derived at read time so an expired
// lease shows up without a background reaper.
func (r *ExecutorRegistry) List() ([]ExecutorEntry, error) {
	ok, err := tableExists(context.Background(), r.db.readDB, "executor_registry")
	if err != nil || !ok {
		return nil, err
	}
	rows, err := r.db.readDB.Query(`SELECT ` + executorColumns + `
		FROM executor_registry ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := r.now().UTC()
	var out []ExecutorEntry
	for rows.Next() {
		e, err := scanExecutor(rows.Scan)
		if err != nil {
			return nil, err
		}
		e.Available = e.availableAt(now)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SelectAvailable returns the first available executor matching the request,
// ordered by id for repeatability. With ExecutorID set it targets one executor;
// a disabled, revoked or expired target yields ErrExecutorUnavailable instead
// of silently falling back to another executor. When no executor matches the
// refusal is the same typed error with an empty id.
func (r *ExecutorRegistry) SelectAvailable(req SelectRequest) (ExecutorEntry, error) {
	if strings.TrimSpace(req.ExecutorID) != "" && strings.TrimSpace(req.ExecutorID) != req.ExecutorID {
		return ExecutorEntry{}, domain.NewAppError(domain.CodeInvalidInput,
			"store: executors: padded executor id")
	}
	entries, err := r.List()
	if err != nil {
		return ExecutorEntry{}, err
	}
	for _, e := range entries {
		if !e.Available {
			continue
		}
		if req.ExecutorID != "" && e.ID != req.ExecutorID {
			continue
		}
		if req.Runtime != "" && e.Runtime != req.Runtime {
			continue
		}
		if !hasCapabilities(e.Capabilities, req.Capabilities) {
			continue
		}
		return e, nil
	}
	reason := "no available executor matches the request"
	if req.ExecutorID != "" {
		reason = "executor is disabled, revoked or past its lease"
	}
	return ExecutorEntry{}, &ExecutorUnavailableError{ID: req.ExecutorID, Reason: reason}
}

// Acquire is the direct request for one named executor. It is SelectAvailable
// restricted to an id, and it is the path that returns the typed
// ErrExecutorUnavailable refusal for a known but unavailable executor.
func (r *ExecutorRegistry) Acquire(id string) (ExecutorEntry, error) {
	return r.SelectAvailable(SelectRequest{ExecutorID: id})
}

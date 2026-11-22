// Package store: durable session registry (BS.66).
//
// A session records that an agent session existed, what state it is in, and
// when it was last seen. The store keeps the status, the creation and
// last-seen instants, and an opaque JSON metadata object. The table lives in
// the same SQLite file as the rest of the state and is created on first use,
// so it needs no migration file. Reads never create the table: on a store
// that has not written a session yet they report an empty result.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// sessionStore is the minimal port the storage conformance suite exercises.
// The SQLite *DB and any alternative adapter implement the same operations, so
// one named set of contracts certifies every adapter without copying tests.
type sessionStore interface {
	SaveSession(s Session) error
	GetSession(id string) (Session, bool, error)
	ListSessions() ([]Session, error)
	FindSessionByTask(task string) (Session, bool, error)
	Touch(id string, at time.Time) error
	CloseSession(id string, at time.Time) error
	DeleteSession(id string) error
}

// Status values a caller normally writes to a session. The store only
// requires a non-empty status so a caller can carry its own state machine.
const (
	SessionStatusActive = "active"
	SessionStatusIdle   = "idle"
	SessionStatusClosed = "closed"
)

// SessionMetaTaskID is the metadata key beacon spawn uses to link a session
// to the task line that created it. The store reads the key without knowing
// which command wrote it (T3.278).
const SessionMetaTaskID = "ai.beacon/taskID"

// createSessionsSQL is issued on first use by every write method. id is the
// stable key a caller uses to find a session again after a restart.
// created_at is set on insert and never rewritten by SaveSession, so it keeps
// the instant the session was first registered.
const createSessionsSQL = `
CREATE TABLE IF NOT EXISTS sessions (
  id         TEXT PRIMARY KEY,
  status     TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL,
  metadata   BLOB NOT NULL
);
`

// Session is one persisted session row. CreatedAt and LastSeen are stored as
// Unix nanoseconds and read back in UTC. Metadata is serialized as JSON; the
// store does not interpret its keys.
type Session struct {
	ID        string
	Status    string
	CreatedAt time.Time
	LastSeen  time.Time
	Metadata  map[string]string
}

// SaveSession inserts s or updates the row with the same id. Status, LastSeen
// and Metadata are overwritten; CreatedAt keeps its value from the insert. A
// zero CreatedAt or LastSeen is filled with the current UTC time.
func (d *DB) SaveSession(s Session) error {
	if d == nil {
		return errors.New("store: sessions: nil db")
	}
	if strings.TrimSpace(s.ID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: sessions: empty id")
	}
	if strings.TrimSpace(s.Status) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: sessions: empty status")
	}
	metadata, err := json.Marshal(s.Metadata)
	if err != nil {
		return fmt.Errorf("store: sessions: encode metadata: %w", err)
	}
	now := time.Now().UTC()
	createdAt := s.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	lastSeen := s.LastSeen
	if lastSeen.IsZero() {
		lastSeen = now
	}
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createSessionsSQL); err != nil {
			return fmt.Errorf("store: sessions: create table: %w", err)
		}
		_, err := tx.Exec(`
			INSERT INTO sessions (id, status, created_at, last_seen, metadata)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
			  status = excluded.status,
			  last_seen = excluded.last_seen,
			  metadata = excluded.metadata`,
			s.ID, s.Status, timeToNano(createdAt), timeToNano(lastSeen), metadata)
		if err != nil {
			return fmt.Errorf("store: sessions: save: %w", err)
		}
		return nil
	})
}

// GetSession reads the session with id. found is false for a store that never
// wrote a session and for a store with no such id; a read never creates the
// table.
func (d *DB) GetSession(id string) (Session, bool, error) {
	if d == nil {
		return Session{}, false, errors.New("store: sessions: nil db")
	}
	if strings.TrimSpace(id) == "" {
		return Session{}, false, domain.NewAppError(domain.CodeInvalidInput, "store: sessions: empty id")
	}
	ctx := context.Background()
	ok, err := tableExists(ctx, d.readDB, "sessions")
	if err != nil || !ok {
		return Session{}, false, err
	}
	s, err := scanSession(d.readDB.QueryRowContext(ctx, `
		SELECT id, status, created_at, last_seen, metadata
		FROM sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	return s, true, nil
}

// ListSessions returns every stored session ordered by creation time, then by
// id so sessions created in the same instant have a stable order. An empty
// store returns an empty slice and no error.
func (d *DB) ListSessions() ([]Session, error) {
	if d == nil {
		return nil, errors.New("store: sessions: nil db")
	}
	ctx := context.Background()
	ok, err := tableExists(ctx, d.readDB, "sessions")
	if err != nil || !ok {
		return nil, err
	}
	rows, err := d.readDB.QueryContext(ctx, `
		SELECT id, status, created_at, last_seen, metadata
		FROM sessions ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// FindSessionByTask returns the oldest open session whose metadata links it
// to task. A task line has one spawn session, so the first match in creation
// order wins. Closed rows are skipped: resuming a task must not hand back the
// id of a session that was already shut down. found is false on a store with
// no such session; the read never creates the table.
func (d *DB) FindSessionByTask(task string) (Session, bool, error) {
	task = strings.TrimSpace(task)
	if task == "" {
		return Session{}, false, domain.NewAppError(domain.CodeInvalidInput, "store: sessions: empty task id")
	}
	all, err := d.ListSessions()
	if err != nil {
		return Session{}, false, err
	}
	for _, s := range all {
		if s.Status == SessionStatusClosed {
			continue
		}
		if s.Metadata[SessionMetaTaskID] == task {
			return s, true, nil
		}
	}
	return Session{}, false, nil
}

// Touch moves id's last_seen to at, or to the current UTC time when at is
// zero. It is the heartbeat a caller writes to say the session is still
// alive. Other fields are left alone. It returns an error when no session has
// that id.
func (d *DB) Touch(id string, at time.Time) error {
	if d == nil {
		return errors.New("store: sessions: nil db")
	}
	if strings.TrimSpace(id) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: sessions: empty id")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createSessionsSQL); err != nil {
			return fmt.Errorf("store: sessions: create table: %w", err)
		}
		res, err := tx.Exec(`UPDATE sessions SET last_seen = ? WHERE id = ?`,
			timeToNano(at), id)
		if err != nil {
			return fmt.Errorf("store: sessions: touch: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: sessions: touch rows: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("store: sessions: unknown id %q", id)
		}
		return nil
	})
}

// CloseSession marks id as closed at at, or at the current UTC time when at
// is zero. The first close writes status and last_seen; a later close leaves
// both untouched. created_at and metadata are never written, so closing a row
// twice changes nothing and cannot add a second row. It returns an error when
// no session has that id.
func (d *DB) CloseSession(id string, at time.Time) error {
	if d == nil {
		return errors.New("store: sessions: nil db")
	}
	if strings.TrimSpace(id) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: sessions: empty id")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createSessionsSQL); err != nil {
			return fmt.Errorf("store: sessions: create table: %w", err)
		}
		res, err := tx.Exec(`
			UPDATE sessions SET
			  status = ?,
			  last_seen = CASE WHEN status = ? THEN last_seen ELSE ? END
			WHERE id = ?`,
			SessionStatusClosed, SessionStatusClosed, timeToNano(at), id)
		if err != nil {
			return fmt.Errorf("store: sessions: close: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: sessions: close rows: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("store: sessions: unknown id %q", id)
		}
		return nil
	})
}

// StaleSessions returns open sessions last seen strictly before before, in
// last-seen then id order. Closed rows are excluded, so a second reap pass
// cannot pick up a session that was already shut down. limit caps the result:
// a non-positive limit returns no rows. The read never creates the table.
func (d *DB) StaleSessions(before time.Time, limit int) ([]Session, error) {
	if d == nil {
		return nil, errors.New("store: sessions: nil db")
	}
	if limit <= 0 {
		return []Session{}, nil
	}
	ctx := context.Background()
	ok, err := tableExists(ctx, d.readDB, "sessions")
	if err != nil || !ok {
		return nil, err
	}
	rows, err := d.readDB.QueryContext(ctx, `
		SELECT id, status, created_at, last_seen, metadata
		FROM sessions
		WHERE status != ? AND last_seen < ?
		ORDER BY last_seen, id
		LIMIT ?`, SessionStatusClosed, timeToNano(before), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// rowScanner is implemented by both *sql.Row and *sql.Rows, so a single
// decoder serves GetSession and ListSessions.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(row rowScanner) (Session, error) {
	var s Session
	var createdAt, lastSeen int64
	var metadata []byte
	if err := row.Scan(&s.ID, &s.Status, &createdAt, &lastSeen, &metadata); err != nil {
		return Session{}, err
	}
	s.CreatedAt = timeFromNano(createdAt)
	s.LastSeen = timeFromNano(lastSeen)
	if err := json.Unmarshal(metadata, &s.Metadata); err != nil {
		return Session{}, fmt.Errorf("store: sessions: decode metadata: %w", err)
	}
	return s, nil
}

// DeleteSession removes id. A missing id or a store that never wrote a
// session is a no-op, so repeated deletes converge on the same state.
func (d *DB) DeleteSession(id string) error {
	if d == nil {
		return errors.New("store: sessions: nil db")
	}
	if strings.TrimSpace(id) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: sessions: empty id")
	}
	ctx := context.Background()
	ok, err := tableExists(ctx, d.readDB, "sessions")
	if err != nil || !ok {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM sessions WHERE id = ?`, id); err != nil {
			return fmt.Errorf("store: sessions: delete: %w", err)
		}
		return nil
	})
}

// Package store: observability for beacon_stats.
//
// This file owns the persist_progress table and the journal load-error
// collector. The migration corpus belongs to T1.7, so the table is created on
// first use, the same way archive.go creates watch_archive.
//
// ObservedPersist is the only place that advances seq. It does so after the
// wrapped write has committed, mirroring "meta.json with seq is written last"
// from WORKER-PROMPT §4.2. A failed write keeps seq where it was and records
// the reason in last_error, so the caller hears PERSIST_FAILED and the stats
// reader sees an honest progress row.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

const createPersistProgressSQL = `
CREATE TABLE IF NOT EXISTS persist_progress (
  id           INTEGER PRIMARY KEY CHECK (id = 1),
  seq          INTEGER NOT NULL DEFAULT 0,
  last_error   TEXT NOT NULL DEFAULT '',
  failed_since INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO persist_progress (id, seq, last_error, failed_since)
VALUES (1, 0, '', 0);
`

// PersistState is the persist{seq,lastError} block of beacon_stats. FailedSince
// is when the last failed write was recorded; it is zero while the last write
// succeeded. The JSON names match the stats contract.
type PersistState struct {
	Seq         uint64    `json:"seq"`
	LastError   string    `json:"lastError"`
	FailedSince time.Time `json:"failedSince,omitempty"`
}

// LoadError is one damaged journal row. Line is the event seq, the SQLite
// analogue of a JSONL line number. Untrusted payload bytes are never copied
// into Reason; only the decoder error is kept.
type LoadError struct {
	Line   int64  `json:"line"`
	Reason string `json:"reason"`
}

// SingleWriter is the minimal write port. *DB and *ObservedPersist both
// satisfy it, so writers can be layered, for example a redacting writer around
// the observed one.
type SingleWriter interface {
	Write(ctx context.Context, fn func(*sql.Tx) error) error
}

var (
	_ SingleWriter = (*DB)(nil)
	_ SingleWriter = (*ObservedPersist)(nil)
)

// ObservedPersist wraps a single writer and keeps persist_progress honest.
type ObservedPersist struct {
	mu     sync.Mutex
	db     *DB
	writer SingleWriter
}

// Observed returns a progress-tracking writer bound to d's own writer.
func (d *DB) Observed() *ObservedPersist { return NewObservedPersist(d) }

// NewObservedPersist wraps db's own writer.
func NewObservedPersist(db *DB) *ObservedPersist {
	return NewObservedPersistWithWriter(db, db)
}

// NewObservedPersistWithWriter wraps w and stores progress in db. Splitting
// the two lets tests inject a failing writer while keeping a healthy progress
// DB.
func NewObservedPersistWithWriter(db *DB, w SingleWriter) *ObservedPersist {
	if w == nil {
		w = db
	}
	return &ObservedPersist{db: db, writer: w}
}

// Write runs fn on the wrapped writer and records the outcome. It never
// reports success when the data write or the progress write failed. A failed
// write keeps seq unchanged and is returned as PERSIST_FAILED.
func (p *ObservedPersist) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}

	if err := p.writer.Write(ctx, fn); err != nil {
		if recErr := p.recordFailure(ctx, err); recErr != nil {
			err = errors.Join(err, recErr)
		}
		return domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	if err := p.recordSuccess(ctx); err != nil {
		return domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	return nil
}

func (p *ObservedPersist) recordFailure(ctx context.Context, cause error) error {
	return p.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createPersistProgressSQL); err != nil {
			return fmt.Errorf("store: persist progress: create table: %w", err)
		}
		_, err := tx.Exec(`
			UPDATE persist_progress
			SET last_error = ?, failed_since = ?
			WHERE id = 1`, cause.Error(), time.Now().UTC().UnixNano())
		return err
	})
}

func (p *ObservedPersist) recordSuccess(ctx context.Context) error {
	return p.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createPersistProgressSQL); err != nil {
			return fmt.Errorf("store: persist progress: create table: %w", err)
		}
		_, err := tx.Exec(`
			UPDATE persist_progress
			SET seq = seq + 1, last_error = '', failed_since = 0
			WHERE id = 1`)
		return err
	})
}

// PersistState reads persist{seq,lastError} without writing. A store that has
// never had an observed write reports a zero state.
func (d *DB) PersistState() (PersistState, error) {
	ok, err := tableExists(context.Background(), d.readDB, "persist_progress")
	if err != nil || !ok {
		return PersistState{}, err
	}
	var (
		state       PersistState
		failedSince int64
	)
	err = d.readDB.QueryRow(`
		SELECT seq, last_error, failed_since FROM persist_progress WHERE id = 1`,
	).Scan(&state.Seq, &state.LastError, &failedSince)
	if errors.Is(err, sql.ErrNoRows) {
		return PersistState{}, nil
	}
	if err != nil {
		return PersistState{}, err
	}
	if failedSince > 0 {
		state.FailedSince = time.Unix(0, failedSince).UTC()
	}
	return state, nil
}

func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name,
	).Scan(&n)
	return n == 1, err
}

// JournalEvent is one well-formed row of the event journal.
type JournalEvent struct {
	Seq           int64           `json:"seq"`
	WatchID       string          `json:"watchID"`
	SchemaVersion int             `json:"schemaVersion"`
	Payload       json.RawMessage `json:"payload"`
	CreatedAt     int64           `json:"createdAt"`
}

// LoadEvents reads the journal in seq order. Malformed rows are returned as
// LoadError and skipped; one damaged row never stops the load or the start.
func (d *DB) LoadEvents() ([]JournalEvent, []LoadError, error) {
	rows, err := d.readDB.Query(`
		SELECT seq, watch_id, schema_version, payload, created_at
		FROM events ORDER BY seq`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var (
		events     []JournalEvent
		loadErrors []LoadError
	)
	for rows.Next() {
		var (
			seq           int64
			watchID       string
			schemaVersion int
			payload       []byte
			createdAt     int64
		)
		if err := rows.Scan(&seq, &watchID, &schemaVersion, &payload, &createdAt); err != nil {
			return nil, nil, err
		}
		var probe any
		if err := json.Unmarshal(payload, &probe); err != nil {
			loadErrors = append(loadErrors, LoadError{Line: seq, Reason: err.Error()})
			continue
		}
		events = append(events, JournalEvent{
			Seq:           seq,
			WatchID:       watchID,
			SchemaVersion: schemaVersion,
			Payload:       json.RawMessage(payload),
			CreatedAt:     createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return events, loadErrors, nil
}

// PersistStats is the read method for beacon_stats. It forms persist and
// loadErrors and never writes: no read path touches persist_progress or
// events.
func (d *DB) PersistStats() (PersistState, []LoadError, error) {
	state, err := d.PersistState()
	if err != nil {
		return PersistState{}, nil, err
	}
	_, loadErrors, err := d.LoadEvents()
	if err != nil {
		return PersistState{}, nil, err
	}
	return state, loadErrors, nil
}

// PersistStats is the same read method on the wrapped writer, for callers that
// hold only the ObservedPersist.
func (p *ObservedPersist) PersistStats() (PersistState, []LoadError, error) {
	return p.db.PersistStats()
}

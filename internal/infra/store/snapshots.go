// Package store: durable state snapshots (BS.18).
//
// A snapshot is the serialized state of watches and counters at one instant,
// written with the schema version of its payload and the UTC time it was
// taken. Snapshots are append-only: each save adds a row and the newest row is
// the current snapshot. The table lives in the same SQLite file as the rest
// of the state, so a snapshot survives a process restart. This file owns only
// the store side; the projection that builds a snapshot belongs to another
// card.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// createSnapshotsSQL owns the snapshots table. Like the other tables owned by
// a single card, it is created on first use instead of in the migration
// corpus. Seq is assigned by SQLite and never reused, so it orders snapshots
// that share a created_at value.
const createSnapshotsSQL = `
CREATE TABLE IF NOT EXISTS snapshots (
  seq            INTEGER PRIMARY KEY AUTOINCREMENT,
  schema_version INTEGER NOT NULL,
  payload        BLOB NOT NULL,
  created_at     INTEGER NOT NULL
);
`

// Snapshot is one durable state snapshot. SchemaVersion records the layout of
// Payload so a reader can tell whether it can restore the bytes. Payload is
// the opaque serialized state; the store does not decode it. CreatedAt is the
// UTC instant the snapshot was taken, and Seq is assigned by SQLite on insert.
type Snapshot struct {
	Seq           int64
	SchemaVersion int
	Payload       []byte
	CreatedAt     time.Time
}

// ensureSnapshots creates the snapshots table inside tx. The DDL and the row
// commit together, so a failed save leaves no table behind on a fresh store.
func ensureSnapshots(tx *sql.Tx) error {
	if _, err := tx.Exec(createSnapshotsSQL); err != nil {
		return fmt.Errorf("store: snapshot: create table: %w", err)
	}
	return nil
}

// SaveSnapshotTx appends one snapshot inside tx. A zero CreatedAt is filled
// with the current UTC time. The row commits or rolls back with the caller's
// transaction, so a projection update and its snapshot are one atomic change.
func SaveSnapshotTx(tx *sql.Tx, s Snapshot) (int64, error) {
	if tx == nil {
		return 0, domain.NewAppError(domain.CodeInvalidInput, "store: snapshot: nil transaction")
	}
	if s.SchemaVersion < 0 {
		return 0, domain.NewAppError(domain.CodeInvalidInput, "store: snapshot: negative schema version")
	}
	if err := ensureSnapshots(tx); err != nil {
		return 0, err
	}
	at := s.CreatedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	res, err := tx.Exec(`
		INSERT INTO snapshots (schema_version, payload, created_at)
		VALUES (?, ?, ?)`, s.SchemaVersion, s.Payload, at.UTC().UnixNano())
	if err != nil {
		return 0, fmt.Errorf("store: snapshot: insert: %w", err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: snapshot: last insert id: %w", err)
	}
	return seq, nil
}

// SaveSnapshot appends one snapshot in its own transaction on the single
// writer. Callers that need the row to commit with another change use
// SaveSnapshotTx instead.
func (d *DB) SaveSnapshot(s Snapshot) (int64, error) {
	var seq int64
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		seq, err = SaveSnapshotTx(tx, s)
		return err
	})
	return seq, err
}

// LatestSnapshot reads the most recent snapshot by created_at, with the insert
// seq as a tie-breaker. A store that never saved a snapshot reports found=false
// and does not create the table: a read never writes.
func (d *DB) LatestSnapshot() (Snapshot, bool, error) {
	ok, err := tableExists(context.Background(), d.readDB, "snapshots")
	if err != nil || !ok {
		return Snapshot{}, false, err
	}
	var (
		s  Snapshot
		at int64
	)
	err = d.readDB.QueryRow(`
		SELECT seq, schema_version, payload, created_at
		FROM snapshots ORDER BY created_at DESC, seq DESC LIMIT 1`,
	).Scan(&s.Seq, &s.SchemaVersion, &s.Payload, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, err
	}
	s.CreatedAt = time.Unix(0, at).UTC()
	return s, true, nil
}

// SnapshotCount returns the number of stored snapshots. It is the direct
// check that an empty store has none and that a rolled-back save left none.
func (d *DB) SnapshotCount() (int, error) {
	ok, err := tableExists(context.Background(), d.readDB, "snapshots")
	if err != nil || !ok {
		return 0, err
	}
	var n int
	err = d.readDB.QueryRow(`SELECT count(*) FROM snapshots`).Scan(&n)
	return n, err
}

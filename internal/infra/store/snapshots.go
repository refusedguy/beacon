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

// RecoveryOutcome is the explicit result of restoring one node from a
// snapshot. The values are stable strings so an operator can read them from a
// restore report. A node always gets one of them; restore never leaves a node
// in an unnamed state.
type RecoveryOutcome string

const (
	// OutcomeResumed reuses the live session and Watch recorded in the
	// snapshot: the worker picks up its in-flight obligation.
	OutcomeResumed RecoveryOutcome = "resumed"
	// OutcomeFreshPrimed starts a fresh worker from a healthy Watch that has
	// no durable session. It is never used to hide a failed resume.
	OutcomeFreshPrimed RecoveryOutcome = "fresh-primed"
	// OutcomeAwaitingDecision holds the node until the orchestrator decides
	// whether to merge the ready Watch or wait.
	OutcomeAwaitingDecision RecoveryOutcome = "awaiting-decision"
	// OutcomeAttentionRequired needs an agent or an operator to act: a failed
	// session, a blocked Watch or a verdict that cannot advance on its own.
	OutcomeAttentionRequired RecoveryOutcome = "attention-required"
	// OutcomeFailed means the node could not be restored at all. It is
	// reported explicitly instead of silently starting a fresh worker
	// (ADR 0113).
	OutcomeFailed RecoveryOutcome = "failed"
)

// RecoveryNode is the per-node input to ClassifyRecovery. Session is nil when
// the node has no persisted session, Watch is nil when the snapshot carried no
// Watch, and Err carries a decode or read failure for the node.
type RecoveryNode struct {
	Session *Session
	Watch   *domain.Watch
	Err     error
}

// verdictNeedsAttention reports whether v is a verdict that cannot advance
// without an agent or an operator: a rebase, a bounded retry, a keep_working
// fix, a timeout or a block. Ready and watching are deliberately excluded.
func verdictNeedsAttention(v domain.Verdict) bool {
	switch v {
	case domain.VerdictRebase, domain.VerdictRetry, domain.VerdictKeepWorking,
		domain.VerdictTimeout, domain.VerdictBlocked:
		return true
	default:
		return false
	}
}

// terminalWatch reports whether w has finished: a merged or closed verdict,
// or a closed status. A finished node has no live work to resume and is left
// for an operator to archive.
func terminalWatch(w domain.Watch) bool {
	return w.Verdict == domain.VerdictMerged || w.Verdict == domain.VerdictClosed ||
		w.Status == domain.StatusClosed
}

// ClassifyRecovery returns the explicit restore outcome for one node. Rules
// are applied in priority order and the first match wins. A failed restore is
// never downgraded to a fresh prime, and a Watch that needs attention or a
// decision is reported as such even when it has no session.
func ClassifyRecovery(node RecoveryNode) RecoveryOutcome {
	if node.Err != nil || node.Watch == nil {
		return OutcomeFailed
	}
	w := node.Watch
	if terminalWatch(*w) {
		return OutcomeAttentionRequired
	}
	if verdictNeedsAttention(w.Verdict) || w.Status == domain.StatusBlocked || w.Blocked != nil {
		return OutcomeAttentionRequired
	}
	if node.Session != nil && node.Session.Status == SessionStatusClosed {
		return OutcomeAttentionRequired
	}
	if w.Verdict == domain.VerdictReady {
		return OutcomeAwaitingDecision
	}
	if node.Session == nil {
		return OutcomeFreshPrimed
	}
	return OutcomeResumed
}

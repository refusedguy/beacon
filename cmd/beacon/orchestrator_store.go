package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// createOrchestratorMergeLedgerSQL owns the durable idempotency ledger. One row
// per (watchID, revision) is enough: a repeated orchestrate pass must not merge
// the same Watch revision twice. The adapter lives here, at the composition
// root, so the orchestrator app package declares no infra import (ARDH-15).
const createOrchestratorMergeLedgerSQL = `
CREATE TABLE IF NOT EXISTS orchestrator_merges (
  watch_id   TEXT NOT NULL,
  revision   INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (watch_id, revision)
)
`

// dbOrchestratorStore adapts Beacon state to the orchestrator's Store port.
// Active Watches come from the injected reader; the ledger and the cancellation
// use the same SQLite file through its single-writer connection.
type dbOrchestratorStore struct {
	reader orchestrator.WatchReader
	db     *store.DB
}

// newDBOrchestratorStore builds the production Store. reader projects the
// domain Watches and db is the state file that holds the ledger and the event
// log.
func newDBOrchestratorStore(reader orchestrator.WatchReader, db *store.DB) *dbOrchestratorStore {
	return &dbOrchestratorStore{reader: reader, db: db}
}

var (
	_ orchestrator.Store          = (*dbOrchestratorStore)(nil)
	_ orchestrator.WatchHolder    = (*dbOrchestratorStore)(nil)
	_ orchestrator.MergedPRReader = (*dbOrchestratorStore)(nil)
)

// ActiveWatches delegates to the reader built at the composition root.
func (s *dbOrchestratorStore) ActiveWatches(ctx context.Context) ([]orchestrator.Watch, error) {
	if s == nil || s.reader == nil {
		return nil, fmt.Errorf("orchestrator: watch reader is nil")
	}
	return s.reader.ActiveWatches(ctx)
}

// WasMerged reports whether (watchID, revision) is already in the ledger.
// store.DB exposes writes but no generic read port, so the SELECT runs inside
// Write on the same single-writer connection; the statement is read-only.
func (s *dbOrchestratorStore) WasMerged(ctx context.Context, watchID string, revision uint64) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("orchestrator: merge store is nil")
	}
	var found bool
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createOrchestratorMergeLedgerSQL); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(`
			SELECT count(*) FROM orchestrator_merges
			WHERE watch_id = ? AND revision = ?`, watchID, revision,
		).Scan(&n); err != nil {
			return err
		}
		found = n > 0
		return nil
	})
	return found, err
}

// RecordMerge inserts (watchID, revision) into the ledger. A repeat is a
// no-op, so the ledger itself is idempotent under redelivery.
func (s *dbOrchestratorStore) RecordMerge(ctx context.Context, watchID string, revision uint64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("orchestrator: merge store is nil")
	}
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createOrchestratorMergeLedgerSQL); err != nil {
			return err
		}
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO orchestrator_merges (watch_id, revision, created_at)
			VALUES (?, ?, ?)`, watchID, revision, time.Now().UTC().UnixNano())
		return err
	})
}

// RecordFixAttempt persists one FIX agent attempt for a blocked Watch. The
// domain method is the pure copy that owns the increment, so the counter lives
// on the block record and an unblock clears it with the rest of the record.
// A Watch that was unblocked between projection and this call is left alone.
func (s *dbOrchestratorStore) RecordFixAttempt(ctx context.Context, watchID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("orchestrator: fix attempt store is nil")
	}
	_ = ctx
	w, err := s.db.Watch(watchID)
	if err != nil {
		return fmt.Errorf("orchestrator: load watch %s for fix attempt: %w", watchID, err)
	}
	next := w.RecordFixAttempt()
	if next.Blocked == nil {
		return nil
	}
	return s.db.SaveWatch(next)
}

// CloseWatch marks a Watch closed as merged. The periodic pass must not route
// through mcp.NewCancelWatcher: that records closeReason="cancelled" for a PR
// the hosting service actually merged. Setting the three fields directly keeps
// the audit reason honest; a Watch that is already closed is left untouched.
func (s *dbOrchestratorStore) CloseWatch(ctx context.Context, watchID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("orchestrator: watch store is nil")
	}
	_ = ctx
	w, err := s.db.Watch(watchID)
	if err != nil {
		return fmt.Errorf("orchestrator: load watch %s for close: %w", watchID, err)
	}
	if w.Status == "closed" {
		return nil
	}
	w.Status = domain.StatusClosed
	w.Verdict = domain.VerdictMerged
	w.CloseReason = "merged"
	return s.db.SaveWatch(w)
}

// MergedPR implements MergedPRReader from the Watch rows the process already
// has in SQLite. Both the active table and the archive are checked: a PR merged
// more than the retention window ago still satisfies a waitFor reference. No
// hosting-service call is made here.
func (s *dbOrchestratorStore) MergedPR(ctx context.Context, pr int) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("orchestrator: merge store is nil")
	}
	_ = ctx
	all, err := s.db.AllWatches()
	if err != nil {
		return false, fmt.Errorf("orchestrator: watches for PR %d: %w", pr, err)
	}
	for _, w := range all {
		if w.PR == pr && (string(w.Verdict) == "merged" || w.CloseReason == "merged") {
			return true, nil
		}
	}
	archived, err := s.db.ArchivedWatches()
	if err != nil {
		return false, fmt.Errorf("orchestrator: archived watches for PR %d: %w", pr, err)
	}
	for _, w := range archived {
		if w.PR == pr && (string(w.Verdict) == "merged" || w.CloseReason == "merged") {
			return true, nil
		}
	}
	return false, nil
}

// HoldWatch persists the hold reason on a Watch whose merge is waiting on a
// dependency. The verdict is visible to beacon_list_running and beacon inspect
// through the same store handle they already read, and no obligation is staged:
// an unsatisfied dependency is nobody's fault and wakes nobody (ADR 0158).
func (s *dbOrchestratorStore) HoldWatch(ctx context.Context, watchID, verdict string) error {
	if s == nil || s.db == nil {
		return errors.New("orchestrator: watch store is nil")
	}
	_ = ctx
	w, err := s.db.Watch(watchID)
	if err != nil {
		return fmt.Errorf("orchestrator: load watch %s for dependency hold: %w", watchID, err)
	}
	if w.Status == "closed" {
		return nil
	}
	w.Verdict = domain.Verdict(verdict)
	return s.db.SaveWatch(w)
}

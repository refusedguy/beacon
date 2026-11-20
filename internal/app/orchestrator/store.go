package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// createMergeLedgerSQL owns the durable idempotency ledger. One row per
// (watchID, revision) is enough: a repeated orchestrate pass must not merge
// the same Watch revision twice. Like the other on-first-use tables in the
// state file, it is created on first use so this card adds no migration.
const createMergeLedgerSQL = `
CREATE TABLE IF NOT EXISTS orchestrator_merges (
  watch_id   TEXT NOT NULL,
  revision   INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (watch_id, revision)
)
`

// WatchReader supplies the active Watches projected onto the orchestrator's
// decision shape. The composition root performs the projection from the
// domain store, so this package needs no domain types (ADR 0005).
type WatchReader interface {
	ActiveWatches(ctx context.Context) ([]Watch, error)
}

// DBStore adapts Beacon state to the orchestrator's Store port. Active
// Watches come from the injected reader; the ledger and the cancellation use
// the same SQLite file through its single-writer connection.
type DBStore struct {
	reader WatchReader
	db     *store.DB
}

// NewDBStore builds the production Store. reader projects the domain Watches
// and db is the state file that holds the ledger and the cancel event log.
func NewDBStore(reader WatchReader, db *store.DB) *DBStore {
	return &DBStore{reader: reader, db: db}
}

// ActiveWatches delegates to the reader built at the composition root.
func (s *DBStore) ActiveWatches(ctx context.Context) ([]Watch, error) {
	if s == nil || s.reader == nil {
		return nil, fmt.Errorf("orchestrator: watch reader is nil")
	}
	return s.reader.ActiveWatches(ctx)
}

// WasMerged reports whether (watchID, revision) is already in the ledger.
// store.DB exposes writes but no generic read port, so the SELECT runs inside
// Write on the same single-writer connection; the statement is read-only.
func (s *DBStore) WasMerged(ctx context.Context, watchID string, revision uint64) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("orchestrator: merge store is nil")
	}
	var found bool
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createMergeLedgerSQL); err != nil {
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
func (s *DBStore) RecordMerge(ctx context.Context, watchID string, revision uint64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("orchestrator: merge store is nil")
	}
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createMergeLedgerSQL); err != nil {
			return err
		}
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO orchestrator_merges (watch_id, revision, created_at)
			VALUES (?, ?, ?)`, watchID, revision, time.Now().UTC().UnixNano())
		return err
	})
}

// CloseWatch closes the Watch for every subscriber using the same operation
// as beacon_cancel_watch with scope=all: a domain transition, a persisted
// Watch and one append-only audit event. A nil clock keeps the wall-clock
// default of the cancel handler.
func (s *DBStore) CloseWatch(ctx context.Context, watchID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("orchestrator: watch store is nil")
	}
	canceller := mcp.NewCancelWatcher(s.db, mcp.NewSQLCancelEventLog(s.db), nil)
	return canceller.Cancel(ctx, watchID, mcp.CancelScopeAll, "")
}

// mergeFields mirrors the domain MergePolicy persisted under "mergePolicy".
// The pointer on MergePolicy distinguishes an absent key (default policy) from
// an explicit autoMerge=false that must block the merge.
type mergeFields struct {
	AutoMerge       bool     `json:"autoMerge"`
	RequireApproval bool     `json:"requireApproval"`
	WaitFor         []string `json:"waitFor"`
	WaitForMain     []string `json:"waitForMain"`
}

type policyCarrier struct {
	MergePolicy *mergeFields `json:"mergePolicy"`
}

// PolicyFromPayload parses the autor-merge policy of a serialized Watch.
// An absent mergePolicy leaves the default "on" in place, matching
// domain.DefaultMergePolicy. Explicit hold-backs disable the merge.
func PolicyFromPayload(raw []byte) *AutoMergePolicy {
	var c policyCarrier
	if err := json.Unmarshal(raw, &c); err != nil {
		p := DefaultAutoMergePolicy()
		return &p
	}
	if c.MergePolicy == nil {
		p := DefaultAutoMergePolicy()
		return &p
	}
	m := c.MergePolicy
	autoMerge := m.AutoMerge
	return &AutoMergePolicy{
		AutoMerge:       &autoMerge,
		RequireApproval: m.RequireApproval,
		WaitFor:         m.WaitFor,
		WaitForMain:     m.WaitForMain,
	}
}

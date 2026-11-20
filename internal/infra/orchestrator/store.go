package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
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

// DBStore adapts the Beacon SQLite store to the orchestrator's Store port.
// Active Watches come from the domain port; the ledger and the cancellation
// use the same database through its single-writer connection.
type DBStore struct {
	watches domain.Store
	db      *store.DB
	clock   domain.Clock
}

// NewDBStore builds the production Store. A nil clock falls back to the wall
// clock used by beacon_cancel_watch.
func NewDBStore(watches domain.Store, db *store.DB, clock domain.Clock) *DBStore {
	if clock == nil {
		clock = domainClock{}
	}
	return &DBStore{watches: watches, db: db, clock: clock}
}

type domainClock struct{}

func (domainClock) Now() time.Time { return time.Now() }

// ActiveWatches reads the domain store and projects each Watch onto the
// orchestrator's decision shape. The autor-merge fields are optional in the
// persisted payload; when absent they take the default of auto-merge on.
func (s *DBStore) ActiveWatches(ctx context.Context) ([]Watch, error) {
	if s == nil || s.watches == nil {
		return nil, fmt.Errorf("orchestrator: watch store is nil")
	}
	all, err := s.watches.ActiveWatches()
	if err != nil {
		return nil, err
	}
	out := make([]Watch, 0, len(all))
	for _, w := range all {
		out = append(out, Watch{
			ID:       w.ID,
			Repo:     w.Repo,
			PR:       w.PR,
			Verdict:  string(w.Verdict),
			Revision: w.Revision,
			Policy:   policyOf(w),
		})
	}
	return out, nil
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
// Watch and one append-only audit event.
func (s *DBStore) CloseWatch(ctx context.Context, watchID string) error {
	if s == nil || s.watches == nil || s.db == nil {
		return fmt.Errorf("orchestrator: watch store is nil")
	}
	canceller := mcp.NewCancelWatcher(s.watches, mcp.NewSQLCancelEventLog(s.db), s.clock)
	return canceller.Cancel(ctx, watchID, mcp.CancelScopeAll, "")
}

// policyCarrier reads the optional autor-merge keys from the stored Watch
// payload. Both top-level and nested-under-policy spellings are accepted,
// because the domain Watch has no fields for them yet. nil booleans mean the
// key was absent and leave the default in place.
type policyCarrier struct {
	AutoMerge       *bool  `json:"autoMerge"`
	RequireApproval bool   `json:"requireApproval"`
	WaitFor         string `json:"waitFor"`
	WaitForMain     bool   `json:"waitForMain"`
	Policy          struct {
		AutoMerge       *bool  `json:"autoMerge"`
		RequireApproval bool   `json:"requireApproval"`
		WaitFor         string `json:"waitFor"`
		WaitForMain     bool   `json:"waitForMain"`
	} `json:"policy"`
}

func policyOf(w domain.Watch) *AutoMergePolicy {
	raw, err := json.Marshal(w)
	if err != nil {
		p := DefaultAutoMergePolicy()
		return &p
	}
	var c policyCarrier
	if err := json.Unmarshal(raw, &c); err != nil {
		p := DefaultAutoMergePolicy()
		return &p
	}

	var autoMerge *bool
	if c.Policy.AutoMerge != nil {
		autoMerge = c.Policy.AutoMerge
	} else if c.AutoMerge != nil {
		autoMerge = c.AutoMerge
	}
	requireApproval := c.RequireApproval || c.Policy.RequireApproval
	waitFor := c.WaitFor
	if c.Policy.WaitFor != "" {
		waitFor = c.Policy.WaitFor
	}
	waitForMain := c.WaitForMain || c.Policy.WaitForMain

	return &AutoMergePolicy{
		AutoMerge:       autoMerge,
		RequireApproval: requireApproval,
		WaitFor:         waitFor,
		WaitForMain:     waitForMain,
	}
}

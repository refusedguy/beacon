// Package store: project-scoped watch bindings with revision fencing.
//
// A binding ties one Watch revision to the executor that waits on it. The
// (watch_id, revision) key gives one row per revision: a repeat with the same
// key changes nothing, and a new revision adds its own row (T5.25, ADR 0088).
// The fencing token stops a delayed registration from rolling the binding
// back. A write older than the newest stored revision or token is rejected
// instead of being inserted, so wake never follows a stale subscriber
// (ADR 0088, T5.37).
//
// The table has one DDL source: migrations/0005_agent_registry.sql. Startup
// applies it as a migration and the first-use path executes the same embedded
// file (ARDH-02). Both routes create a dedicated subscriber_id column and an
// INTEGER fencing_token, so the inserts below need no schema detection.
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

// ErrStaleWatchBinding reports a write whose revision or fencing token is
// older than the newest stored binding for the watch. Callers can treat it as
// a lost race instead of a corrupt request.
var ErrStaleWatchBinding = errors.New("store: binding: stale watch binding")

const insertWatchBindingSQL = `
INSERT OR IGNORE INTO watch_bindings
  (project_key, watch_id, revision, agent_id, config_id,
   fencing_token, subscriber_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

// WatchBindingEntry is one stored watch-to-executor binding. Revision is
// uint64 in the domain and INTEGER in SQLite; the conversion is guarded.
// FencingToken defaults to Revision when the caller leaves it at zero.
type WatchBindingEntry struct {
	ProjectKey   string
	WatchID      string
	Revision     uint64
	AgentID      string
	ConfigID     string
	FencingToken int64
	SubscriberID string
	CreatedAt    int64
}

// WatchBindingWrite is the outcome of one binding write. Created is false
// when the (watch_id, revision) row already existed; Entry is then the stored
// row, not the incoming one.
type WatchBindingWrite struct {
	Entry   WatchBindingEntry
	Created bool
}

func validateWatchBindingEntry(e WatchBindingEntry) error {
	if strings.TrimSpace(e.ProjectKey) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: binding: empty project key")
	}
	if strings.TrimSpace(e.WatchID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: binding: empty watch id")
	}
	if strings.TrimSpace(e.AgentID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: binding: empty agent id")
	}
	if err := validateRevision(e.Revision); err != nil {
		return err
	}
	if e.FencingToken < 0 {
		return domain.NewAppError(domain.CodeInvalidInput, "store: binding: negative fencing token")
	}
	return nil
}

func ensureWatchBindingTable(tx *sql.Tx) error {
	return createRegistrySchema(tx)
}

// columnQuerier is satisfied by both *sql.Tx and *sql.DB.
type columnQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// storedWatchBindings is the current state of one watch, read inside the write
// transaction so no other writer can slip a newer revision between the check
// and the insert.
type storedWatchBindings struct {
	maxRevision uint64
	maxToken    int64
	byRevision  map[uint64]WatchBindingEntry
}

func readWatchBindingState(q columnQuerier, watchID string) (storedWatchBindings, error) {
	// Migration 0005 stores fencing_token as INTEGER, so read it in its
	// stored type instead of casting it on every binding read (BCX-33).
	query := `
SELECT project_key, revision, agent_id, config_id,
       fencing_token, subscriber_id, created_at
FROM watch_bindings WHERE watch_id = ?`

	rows, err := q.Query(query, watchID)
	if err != nil {
		return storedWatchBindings{}, fmt.Errorf("store: binding: read state: %w", err)
	}
	defer rows.Close()

	state := storedWatchBindings{byRevision: make(map[uint64]WatchBindingEntry)}
	for rows.Next() {
		var e WatchBindingEntry
		var revision, token int64
		dest := []any{&e.ProjectKey, &revision, &e.AgentID, &e.ConfigID, &token,
			&e.SubscriberID, &e.CreatedAt}
		if err := rows.Scan(dest...); err != nil {
			return storedWatchBindings{}, fmt.Errorf("store: binding: scan state: %w", err)
		}
		if revision < 0 {
			return storedWatchBindings{}, fmt.Errorf("store: binding: negative stored revision %d", revision)
		}
		e.WatchID = watchID
		e.Revision = uint64(revision)
		e.FencingToken = token
		if e.SubscriberID == "" {
			e.SubscriberID = e.AgentID
		}
		state.byRevision[e.Revision] = e
		if e.Revision > state.maxRevision {
			state.maxRevision = e.Revision
		}
		if token > state.maxToken {
			state.maxToken = token
		}
	}
	if err := rows.Err(); err != nil {
		return storedWatchBindings{}, err
	}
	return state, nil
}

// WriteWatchBinding writes one binding. The write is idempotent by
// (watch_id, revision) and fenced: a repeat of a stored revision returns the
// old row, while a revision or token below the stored maximum returns
// ErrStaleWatchBinding. Both checks and the insert share one transaction.
func (d *DB) WriteWatchBinding(e WatchBindingEntry) (WatchBindingWrite, error) {
	if err := validateWatchBindingEntry(e); err != nil {
		return WatchBindingWrite{}, err
	}
	if e.FencingToken == 0 {
		e.FencingToken = int64(e.Revision)
	}
	if e.SubscriberID == "" {
		e.SubscriberID = e.AgentID
	}

	now := time.Now().UTC().UnixNano()
	var out WatchBindingWrite
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureWatchBindingTable(tx); err != nil {
			return err
		}
		state, err := readWatchBindingState(tx, e.WatchID)
		if err != nil {
			return err
		}
		if prior, ok := state.byRevision[e.Revision]; ok {
			out = WatchBindingWrite{Entry: prior, Created: false}
			return nil
		}
		if e.Revision < state.maxRevision || e.FencingToken < state.maxToken {
			return fmt.Errorf("%w: watch %s revision %d token %d, stored revision %d token %d",
				ErrStaleWatchBinding, e.WatchID, e.Revision, e.FencingToken,
				state.maxRevision, state.maxToken)
		}

		res, err := tx.Exec(insertWatchBindingSQL,
			e.ProjectKey, e.WatchID, int64(e.Revision), e.AgentID,
			e.ConfigID, e.FencingToken, e.SubscriberID, now)
		if err != nil {
			return fmt.Errorf("store: binding: insert: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: binding: rows affected: %w", err)
		}
		e.CreatedAt = now
		out = WatchBindingWrite{Entry: e, Created: n == 1}
		return nil
	})
	if err != nil {
		return WatchBindingWrite{}, err
	}
	return out, nil
}

// RegisterWatchExecutor records the executor and Watch binding produced by one
// watch_pr call (ADR 0088). It adapts the neutral values passed by the MCP
// boundary to the registry rows and calls RegisterExecutor, which commits both
// rows in one transaction. A repeat with the same Watch revision writes no new
// row. The repo slug is the project key, so a registration is visible to the
// registry reads of the same project.
func (d *DB) RegisterWatchExecutor(projectKey, watchID, subscriberID, role, runtime, adapter, target, configID string, revision uint64) error {
	_, err := d.RegisterExecutor(ExecutorRecord{
		ProjectKey:    projectKey,
		AgentID:       subscriberID,
		Role:          role,
		Runtime:       runtime,
		SessionID:     subscriberID,
		NotifyAdapter: adapter,
		NotifyTarget:  target,
		ConfigID:      configID,
		Status:        RegistryStatusActive,
	}, WatchBinding{
		WatchID:      watchID,
		Revision:     revision,
		ProjectKey:   projectKey,
		AgentID:      subscriberID,
		ConfigID:     configID,
		FencingToken: "0",
	})
	return err
}

// LatestWatchBinding returns the binding with the highest stored revision for
// a watch. It is the row a wake should address; older revisions are kept for
// audit but never selected.
func (d *DB) LatestWatchBinding(watchID string) (WatchBindingEntry, bool, error) {
	if strings.TrimSpace(watchID) == "" {
		return WatchBindingEntry{}, false, domain.NewAppError(
			domain.CodeInvalidInput, "store: binding: empty watch id")
	}
	ok, err := tableExists(context.Background(), d.readDB, "watch_bindings")
	if err != nil || !ok {
		return WatchBindingEntry{}, false, err
	}
	state, err := readWatchBindingState(d.readDB, watchID)
	if err != nil {
		return WatchBindingEntry{}, false, err
	}
	entry, found := state.byRevision[state.maxRevision]
	return entry, found, nil
}

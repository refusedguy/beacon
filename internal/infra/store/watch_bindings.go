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
// The table normally arrives with T5.21's migration. This file also creates
// it on first use, so a tree without that migration still compiles and runs.
// The insert adapts to both schema variants: the migration has a dedicated
// subscriber_id column, while an older registry stores the subscriber in
// agent_id only.
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

const createWatchBindingTableSQL = `
CREATE TABLE IF NOT EXISTS watch_bindings (
  watch_id      TEXT NOT NULL,
  revision      INTEGER NOT NULL,
  project_key   TEXT NOT NULL,
  agent_id      TEXT NOT NULL,
  config_id     TEXT NOT NULL DEFAULT '',
  fencing_token INTEGER NOT NULL DEFAULT 0,
  subscriber_id TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL,
  PRIMARY KEY (watch_id, revision)
);
CREATE INDEX IF NOT EXISTS watch_bindings_by_project_agent
  ON watch_bindings(project_key, agent_id);
`

const (
	insertWatchBindingWithSubscriberSQL = `
INSERT OR IGNORE INTO watch_bindings
  (project_key, watch_id, revision, agent_id, config_id,
   fencing_token, subscriber_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	insertWatchBindingSQL = `
INSERT OR IGNORE INTO watch_bindings
  (project_key, watch_id, revision, agent_id, config_id,
   fencing_token, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`
)

// maxBindingSQLiteInt64 is the largest revision or token that fits a SQLite
// INTEGER. The check keeps a uint64 from wrapping negative on its way to the
// driver.
const maxBindingSQLiteInt64 = int64(^uint64(0) >> 1)

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
	if e.Revision > uint64(maxBindingSQLiteInt64) {
		return domain.NewAppError(domain.CodeInvalidInput, "store: binding: revision out of range")
	}
	if e.FencingToken < 0 {
		return domain.NewAppError(domain.CodeInvalidInput, "store: binding: negative fencing token")
	}
	return nil
}

func ensureWatchBindingTable(tx *sql.Tx) error {
	if _, err := tx.Exec(createWatchBindingTableSQL); err != nil {
		return fmt.Errorf("store: binding: create table: %w", err)
	}
	return nil
}

// columnQuerier is satisfied by both *sql.Tx and *sql.DB.
type columnQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// hasWatchBindingColumn detects the schema variant in use. The migration
// (T5.21) has subscriber_id; an older registry-created table does not.
func hasWatchBindingColumn(q columnQuerier, name string) (bool, error) {
	rows, err := q.Query(`PRAGMA table_info(watch_bindings)`)
	if err != nil {
		return false, fmt.Errorf("store: binding: table info: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var column, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &column, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return false, fmt.Errorf("store: binding: scan table info: %w", err)
		}
		if strings.EqualFold(column, name) {
			return true, rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

// storedWatchBindings is the current state of one watch, read inside the write
// transaction so no other writer can slip a newer revision between the check
// and the insert.
type storedWatchBindings struct {
	maxRevision uint64
	maxToken    int64
	byRevision  map[uint64]WatchBindingEntry
}

func readWatchBindingState(q columnQuerier, watchID string, hasSubscriber bool) (storedWatchBindings, error) {
	query := `
SELECT project_key, revision, agent_id, config_id,
       CAST(fencing_token AS INTEGER), created_at
FROM watch_bindings WHERE watch_id = ?`
	if hasSubscriber {
		query = `
SELECT project_key, revision, agent_id, config_id,
       CAST(fencing_token AS INTEGER), subscriber_id, created_at
FROM watch_bindings WHERE watch_id = ?`
	}

	rows, err := q.Query(query, watchID)
	if err != nil {
		return storedWatchBindings{}, fmt.Errorf("store: binding: read state: %w", err)
	}
	defer rows.Close()

	state := storedWatchBindings{byRevision: make(map[uint64]WatchBindingEntry)}
	for rows.Next() {
		var e WatchBindingEntry
		var revision, token int64
		var subscriber sql.NullString
		dest := []any{&e.ProjectKey, &revision, &e.AgentID, &e.ConfigID, &token}
		if hasSubscriber {
			dest = append(dest, &subscriber)
		}
		dest = append(dest, &e.CreatedAt)
		if err := rows.Scan(dest...); err != nil {
			return storedWatchBindings{}, fmt.Errorf("store: binding: scan state: %w", err)
		}
		if revision < 0 {
			return storedWatchBindings{}, fmt.Errorf("store: binding: negative stored revision %d", revision)
		}
		e.WatchID = watchID
		e.Revision = uint64(revision)
		e.FencingToken = token
		e.SubscriberID = subscriber.String
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
		hasSubscriber, err := hasWatchBindingColumn(tx, "subscriber_id")
		if err != nil {
			return err
		}
		state, err := readWatchBindingState(tx, e.WatchID, hasSubscriber)
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

		var res sql.Result
		if hasSubscriber {
			res, err = tx.Exec(insertWatchBindingWithSubscriberSQL,
				e.ProjectKey, e.WatchID, int64(e.Revision), e.AgentID,
				e.ConfigID, e.FencingToken, e.SubscriberID, now)
		} else {
			res, err = tx.Exec(insertWatchBindingSQL,
				e.ProjectKey, e.WatchID, int64(e.Revision), e.AgentID,
				e.ConfigID, e.FencingToken, now)
		}
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
	hasSubscriber, err := hasWatchBindingColumn(d.readDB, "subscriber_id")
	if err != nil {
		return WatchBindingEntry{}, false, err
	}
	state, err := readWatchBindingState(d.readDB, watchID, hasSubscriber)
	if err != nil {
		return WatchBindingEntry{}, false, err
	}
	entry, found := state.byRevision[state.maxRevision]
	return entry, found, nil
}

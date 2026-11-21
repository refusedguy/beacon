// Package store: project-scoped executor registry and watch bindings.
//
// This file owns the agent_registry and watch_bindings tables. The migration
// corpus belongs to another card (T5.21), so both tables are created on first
// use, the same way archive.go, observability.go and receipts.go own theirs.
// No caller outside this package needs to know when the tables appeared.
//
// The registry stores one executor per (project_key, agent_id) pair. A repeated
// register with the same pair is an upsert: it refreshes the mutable fields and
// never inserts a second row (ADR 0088, eval r01). The agent_id is the opaque
// identity from the caller; in practice it carries the sessionID, because a
// session is what a wake addresses (ADR 0090, T5.33). The project_key keeps
// records from two projects with the same folder name apart (ADR 0086, eval
// r03) and may never be empty.
//
// A watch binding ties one Watch revision to the executor registered with it.
// Its key is (watch_id, revision): two calls for the same revision collapse
// into one row, while a new revision gets its own row and never rewrites an
// older one (T5.25). Both writes run in one transaction, so a register call
// cannot leave an executor without its binding, or the reverse.
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

// Registry statuses from ADR 0090. They describe whether the executor can
// receive a wake; they are independent of the Watch verdict.
const (
	RegistryStatusActive  = "active"
	RegistryStatusStale   = "stale"
	RegistryStatusStopped = "stopped"
)

// createAgentRegistrySQL owns the executor table. created_at is written once
// and preserved by the upsert; updated_at moves on every refresh. The index
// mirrors the (project_key, status) index from the migration, so a registry
// created here has the same access path as one created by T5.21.
const createAgentRegistrySQL = `
CREATE TABLE IF NOT EXISTS agent_registry (
  project_key    TEXT NOT NULL,
  agent_id       TEXT NOT NULL,
  role           TEXT NOT NULL,
  runtime        TEXT NOT NULL DEFAULT '',
  session_id     TEXT NOT NULL DEFAULT '',
  notify_adapter TEXT NOT NULL DEFAULT '',
  notify_target  TEXT NOT NULL DEFAULT '',
  capabilities   BLOB NOT NULL DEFAULT '[]',
  config_id      TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL DEFAULT 'active',
  last_seen      INTEGER NOT NULL DEFAULT 0,
  worktree       TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  PRIMARY KEY (project_key, agent_id)
);
CREATE INDEX IF NOT EXISTS agent_registry_by_project_status
  ON agent_registry(project_key, status);
`

// createWatchBindingsSQL owns the binding table. config_id, fencing_token and
// subscriber_id are present so the migration schema and the on-first-use schema
// accept the same insert without changing either one at write time.
const createWatchBindingsSQL = `
CREATE TABLE IF NOT EXISTS watch_bindings (
  watch_id      TEXT NOT NULL,
  revision      INTEGER NOT NULL,
  project_key   TEXT NOT NULL,
  agent_id      TEXT NOT NULL,
  config_id     TEXT NOT NULL DEFAULT '',
  fencing_token TEXT NOT NULL DEFAULT '',
  subscriber_id TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL,
  PRIMARY KEY (watch_id, revision)
);
CREATE INDEX IF NOT EXISTS watch_bindings_by_executor
  ON watch_bindings(project_key, agent_id);
`

// ExecutorRecord is one registered executor. AgentID is the primary key inside
// a project; SessionID is the opaque delivery address and may be empty when a
// caller has no live session yet. Runtime names the agent's runtime and may be
// empty when the caller does not declare one. Capabilities are stored as JSON
// so a later reader can build the derived capability index (ADR 0089).
type ExecutorRecord struct {
	ProjectKey    string
	AgentID       string
	Role          string
	Runtime       string
	SessionID     string
	NotifyAdapter string
	NotifyTarget  string
	Capabilities  []string
	ConfigID      string
	Status        string
	LastSeen      int64
	Worktree      string
	CreatedAt     int64
	UpdatedAt     int64
}

// WatchBinding ties a Watch revision to an executor. Revision is uint64 in the
// domain, SQLite stores it as an INTEGER; the conversion is guarded below.
type WatchBinding struct {
	WatchID      string
	Revision     uint64
	ProjectKey   string
	AgentID      string
	ConfigID     string
	FencingToken string
	CreatedAt    int64
}

// UpsertResult reports what an executor upsert did. Created is true only for
// the first call with a given (project_key, agent_id). A repeat reports
// Created=false and leaves CreatedAt untouched.
type UpsertResult struct {
	Executor ExecutorRecord
	Created  bool
}

// BindResult reports what a binding write did. Created is false when a row
// with the same (watch_id, revision) already existed.
type BindResult struct {
	Binding WatchBinding
	Created bool
}

// RegisterResult is the combined outcome of one register call: the executor
// upsert and the binding write committed in the same transaction.
type RegisterResult struct {
	Executor    ExecutorRecord
	Binding     WatchBinding
	ExecutorNew bool
	BindingNew  bool
}

// ensureRegistryTables creates both tables inside the caller's transaction.
// It is safe to call on every write; IF NOT EXISTS makes a repeat free.
func ensureRegistryTables(tx *sql.Tx) error {
	for _, stmt := range []string{createAgentRegistrySQL, createWatchBindingsSQL} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("store: registry: create table: %w", err)
		}
	}
	// A table created by migration 0005 predates the sync columns. CREATE TABLE
	// IF NOT EXISTS cannot add them, and the read paths below select them by
	// name, so add what is missing here on every write. PRAGMA table_info is a
	// read and the ALTER runs in the caller's transaction, which makes the
	// whole guard idempotent and safe against a second writer.
	for _, col := range []struct{ name, def string }{
		{"last_seen", "INTEGER NOT NULL DEFAULT 0"},
		{"worktree", "TEXT NOT NULL DEFAULT ''"},
	} {
		has, err := registryHasColumn(tx, col.name)
		if err != nil {
			return fmt.Errorf("store: registry: check %s: %w", col.name, err)
		}
		if has {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE agent_registry ADD COLUMN %s %s", col.name, col.def)
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("store: registry: add %s: %w", col.name, err)
		}
	}
	return nil
}

func validateExecutor(e ExecutorRecord) error {
	if strings.TrimSpace(e.ProjectKey) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: registry: empty project key")
	}
	if strings.TrimSpace(e.AgentID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: registry: empty agent id")
	}
	return nil
}

func validateBinding(b WatchBinding) error {
	if strings.TrimSpace(b.ProjectKey) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: binding: empty project key")
	}
	if strings.TrimSpace(b.AgentID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: binding: empty agent id")
	}
	if strings.TrimSpace(b.WatchID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: binding: empty watch id")
	}
	if err := validateRevision(b.Revision); err != nil {
		return err
	}
	return nil
}

// maxSQLiteInt64 is the largest revision that fits a SQLite INTEGER. The
// value is a compile-time constant so no uint64 can silently wrap to negative
// when it reaches the database driver.
const maxSQLiteInt64 = int64(^uint64(0) >> 1)

// validateRevision rejects a revision that would wrap when stored as a SQLite
// INTEGER. Both binding validators call it so the range check has one home.
func validateRevision(revision uint64) error {
	if revision > uint64(maxSQLiteInt64) {
		return domain.NewAppError(domain.CodeInvalidInput, "store: binding: revision out of range")
	}
	return nil
}

func encodeCapabilities(caps []string) []byte {
	if len(caps) == 0 {
		return []byte("[]")
	}
	b, err := json.Marshal(caps)
	if err != nil {
		return []byte("[]")
	}
	return b
}

func decodeCapabilities(raw []byte) []string {
	var caps []string
	if err := json.Unmarshal(raw, &caps); err != nil {
		return []string{}
	}
	if caps == nil {
		return []string{}
	}
	return caps
}

func normalizeExecutor(e ExecutorRecord, now int64) ExecutorRecord {
	if e.Role == "" {
		e.Role = string(domain.RoleExecutor)
	}
	if e.Status == "" {
		e.Status = RegistryStatusActive
	}
	e.CreatedAt = now
	e.UpdatedAt = now
	return e
}

// executorExists reports whether the row is already present. It runs inside
// the write transaction, so no other writer can slip a row between this check
// and the following upsert.
func executorExists(tx *sql.Tx, e ExecutorRecord) (bool, error) {
	var one int
	err := tx.QueryRow(`
		SELECT 1 FROM agent_registry
		WHERE project_key = ? AND agent_id = ?`,
		e.ProjectKey, e.AgentID,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// upsertExecutorTx refreshes the mutable registration fields. last_seen and
// worktree are inserted for a new row but are not overwritten on conflict: they
// belong to the sync pass, and a later register must not erase the last live
// observation (T5.55).
func upsertExecutorTx(tx *sql.Tx, e ExecutorRecord, now int64) (UpsertResult, error) {
	found, err := executorExists(tx, e)
	if err != nil {
		return UpsertResult{}, fmt.Errorf("store: registry: lookup: %w", err)
	}
	stored := normalizeExecutor(e, now)
	_, err = tx.Exec(`
		INSERT INTO agent_registry
		  (project_key, agent_id, role, runtime, session_id, notify_adapter, notify_target,
		   capabilities, config_id, status, last_seen, worktree, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_key, agent_id) DO UPDATE SET
		  role           = excluded.role,
		  runtime        = excluded.runtime,
		  session_id     = excluded.session_id,
		  notify_adapter = excluded.notify_adapter,
		  notify_target  = excluded.notify_target,
		  capabilities   = excluded.capabilities,
		  config_id      = excluded.config_id,
		  status         = excluded.status,
		  updated_at     = excluded.updated_at`,
		stored.ProjectKey, stored.AgentID, stored.Role, stored.Runtime, stored.SessionID,
		stored.NotifyAdapter, stored.NotifyTarget, encodeCapabilities(stored.Capabilities),
		stored.ConfigID, stored.Status, stored.LastSeen, stored.Worktree,
		stored.CreatedAt, stored.UpdatedAt)
	if err != nil {
		return UpsertResult{}, fmt.Errorf("store: registry: upsert: %w", err)
	}
	if found {
		// created_at is not overwritten by the upsert; read the stored value
		// back so callers see the real row, not the timestamp of this refresh.
		if err := tx.QueryRow(`
			SELECT created_at FROM agent_registry
			WHERE project_key = ? AND agent_id = ?`,
			stored.ProjectKey, stored.AgentID,
		).Scan(&stored.CreatedAt); err != nil {
			return UpsertResult{}, fmt.Errorf("store: registry: read created_at: %w", err)
		}
	}
	return UpsertResult{Executor: stored, Created: !found}, nil
}

func bindWatchTx(tx *sql.Tx, b WatchBinding, now int64) (BindResult, error) {
	res, err := tx.Exec(`
		INSERT OR IGNORE INTO watch_bindings
		  (watch_id, revision, project_key, agent_id, config_id, fencing_token,
		   subscriber_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		b.WatchID, int64(b.Revision), b.ProjectKey, b.AgentID,
		b.ConfigID, b.FencingToken, b.AgentID, now)
	if err != nil {
		return BindResult{}, fmt.Errorf("store: binding: insert: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return BindResult{}, fmt.Errorf("store: binding: rows affected: %w", err)
	}
	created := n == 1
	if created {
		b.CreatedAt = now
	} else {
		if err := tx.QueryRow(`
			SELECT project_key, agent_id, config_id, fencing_token, created_at
			FROM watch_bindings WHERE watch_id = ? AND revision = ?`,
			b.WatchID, int64(b.Revision),
		).Scan(&b.ProjectKey, &b.AgentID, &b.ConfigID, &b.FencingToken, &b.CreatedAt); err != nil {
			return BindResult{}, fmt.Errorf("store: binding: read existing: %w", err)
		}
	}
	return BindResult{Binding: b, Created: created}, nil
}

// UpsertExecutor writes one executor row. A repeat with the same
// (project_key, agent_id) updates the mutable fields and reports Created=false
// while keeping the original created_at.
func (d *DB) UpsertExecutor(e ExecutorRecord) (UpsertResult, error) {
	if err := validateExecutor(e); err != nil {
		return UpsertResult{}, err
	}
	now := time.Now().UTC().UnixNano()
	var out UpsertResult
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureRegistryTables(tx); err != nil {
			return err
		}
		var err error
		out, err = upsertExecutorTx(tx, e, now)
		return err
	})
	if err != nil {
		return UpsertResult{}, err
	}
	return out, nil
}

// BindWatch writes one watch binding. The (watch_id, revision) key makes the
// write idempotent: a repeat inserts nothing and reports Created=false.
func (d *DB) BindWatch(b WatchBinding) (BindResult, error) {
	if err := validateBinding(b); err != nil {
		return BindResult{}, err
	}
	now := time.Now().UTC().UnixNano()
	var out BindResult
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureRegistryTables(tx); err != nil {
			return err
		}
		var err error
		out, err = bindWatchTx(tx, b, now)
		return err
	})
	if err != nil {
		return BindResult{}, err
	}
	return out, nil
}

// RegisterExecutor writes the executor row and its watch binding in one
// transaction. This is the register step of beacon_watch_pr: a repeated call
// with the same executor and revision produces one row in each table, and a
// call with a different sessionID (agent_id) produces a second executor row
// without disturbing the first (ADR 0088, eval r01).
func (d *DB) RegisterExecutor(e ExecutorRecord, b WatchBinding) (RegisterResult, error) {
	if err := validateExecutor(e); err != nil {
		return RegisterResult{}, err
	}
	if err := validateBinding(b); err != nil {
		return RegisterResult{}, err
	}
	if b.ProjectKey != e.ProjectKey || b.AgentID != e.AgentID {
		return RegisterResult{}, domain.NewAppError(domain.CodeInvalidInput,
			"store: registry: binding does not belong to executor")
	}
	now := time.Now().UTC().UnixNano()
	var out RegisterResult
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureRegistryTables(tx); err != nil {
			return err
		}
		u, err := upsertExecutorTx(tx, e, now)
		if err != nil {
			return err
		}
		w, err := bindWatchTx(tx, b, now)
		if err != nil {
			return err
		}
		out = RegisterResult{
			Executor: u.Executor, Binding: w.Binding,
			ExecutorNew: u.Created, BindingNew: w.Created,
		}
		return nil
	})
	if err != nil {
		return RegisterResult{}, err
	}
	return out, nil
}

// Executor returns one registered executor by its project and agent key.
func (d *DB) Executor(projectKey, agentID string) (ExecutorRecord, bool, error) {
	if strings.TrimSpace(projectKey) == "" || strings.TrimSpace(agentID) == "" {
		return ExecutorRecord{}, false, domain.NewAppError(domain.CodeInvalidInput,
			"store: registry: empty project or agent id")
	}
	ok, err := tableExists(context.Background(), d.readDB, "agent_registry")
	if err != nil || !ok {
		return ExecutorRecord{}, false, err
	}
	var e ExecutorRecord
	var caps []byte
	err = d.readDB.QueryRow(`
		SELECT project_key, agent_id, role, runtime, session_id, notify_adapter, notify_target,
		       capabilities, config_id, status, last_seen, worktree, created_at, updated_at
		FROM agent_registry WHERE project_key = ? AND agent_id = ?`,
		projectKey, agentID,
	).Scan(&e.ProjectKey, &e.AgentID, &e.Role, &e.Runtime, &e.SessionID, &e.NotifyAdapter,
		&e.NotifyTarget, &caps, &e.ConfigID, &e.Status, &e.LastSeen, &e.Worktree,
		&e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutorRecord{}, false, nil
	}
	if err != nil {
		return ExecutorRecord{}, false, err
	}
	e.Capabilities = decodeCapabilities(caps)
	return e, true, nil
}

// WatchBinding returns the stored binding for one watch revision.
func (d *DB) WatchBinding(watchID string, revision uint64) (WatchBinding, bool, error) {
	if strings.TrimSpace(watchID) == "" {
		return WatchBinding{}, false, domain.NewAppError(domain.CodeInvalidInput,
			"store: binding: empty watch id")
	}
	ok, err := tableExists(context.Background(), d.readDB, "watch_bindings")
	if err != nil || !ok {
		return WatchBinding{}, false, err
	}
	var b WatchBinding
	var rev int64
	err = d.readDB.QueryRow(`
		SELECT watch_id, revision, project_key, agent_id, config_id, fencing_token, created_at
		FROM watch_bindings WHERE watch_id = ? AND revision = ?`,
		watchID, int64(revision),
	).Scan(&b.WatchID, &rev, &b.ProjectKey, &b.AgentID, &b.ConfigID,
		&b.FencingToken, &b.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return WatchBinding{}, false, nil
	}
	if err != nil {
		return WatchBinding{}, false, err
	}
	b.Revision = uint64(rev)
	return b, true, nil
}

// Executors returns every executor in one project, ordered by agent id. An
// empty project key is rejected so a caller cannot read across projects (eval
// r03).
func (d *DB) Executors(projectKey string) ([]ExecutorRecord, error) {
	if strings.TrimSpace(projectKey) == "" {
		return nil, domain.NewAppError(domain.CodeInvalidInput,
			"store: registry: empty project key")
	}
	ok, err := tableExists(context.Background(), d.readDB, "agent_registry")
	if err != nil || !ok {
		return nil, err
	}
	rows, err := d.readDB.Query(`
		SELECT project_key, agent_id, role, runtime, session_id, notify_adapter, notify_target,
		       capabilities, config_id, status, last_seen, worktree, created_at, updated_at
		FROM agent_registry WHERE project_key = ? ORDER BY agent_id`, projectKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExecutorRecord
	for rows.Next() {
		var e ExecutorRecord
		var caps []byte
		if err := rows.Scan(&e.ProjectKey, &e.AgentID, &e.Role, &e.Runtime, &e.SessionID,
			&e.NotifyAdapter, &e.NotifyTarget, &caps, &e.ConfigID, &e.Status,
			&e.LastSeen, &e.Worktree, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		e.Capabilities = decodeCapabilities(caps)
		out = append(out, e)
	}
	return out, rows.Err()
}

// EnsureRegistrySchema brings a registry created by an older migration set up
// to the current column set. Reads select last_seen and worktree by name, so a
// caller that only reads an old registry must call this once first. The work
// runs through the writer, which keeps the ALTER under the store's single
// writer contract; a second call is a no-op.
func (d *DB) EnsureRegistrySchema() error {
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		return ensureRegistryTables(tx)
	})
}

// SyncExecutor writes the reconciliation outcome of one executor: its status,
// the UTC nanosecond timestamp of the last live observation and the worktree of
// the session that was observed. lastSeen is supplied by the caller so a repeat
// pass with the same observation writes the same values; worktree may be empty
// for a stale row. The write leaves verdicts and every other column alone.
func (d *DB) SyncExecutor(projectKey, agentID, status, worktree string, lastSeen int64) error {
	if strings.TrimSpace(projectKey) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: registry: empty project key")
	}
	if strings.TrimSpace(agentID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: registry: empty agent id")
	}
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureRegistryTables(tx); err != nil {
			return err
		}
		_, err := tx.Exec(`
			UPDATE agent_registry
			SET status = ?, last_seen = ?, worktree = ?, updated_at = ?
			WHERE project_key = ? AND agent_id = ?`,
			status, lastSeen, worktree, time.Now().UTC().UnixNano(), projectKey, agentID)
		return err
	})
}

// ExecutorCount returns the number of executor rows in one project. A repeated
// upsert must not increase it.
func (d *DB) ExecutorCount(projectKey string) (int, error) {
	if strings.TrimSpace(projectKey) == "" {
		return 0, domain.NewAppError(domain.CodeInvalidInput,
			"store: registry: empty project key")
	}
	ok, err := tableExists(context.Background(), d.readDB, "agent_registry")
	if err != nil || !ok {
		return 0, err
	}
	var n int
	err = d.readDB.QueryRow(`
		SELECT count(*) FROM agent_registry WHERE project_key = ?`, projectKey).Scan(&n)
	return n, err
}

// WatchBindingCount returns the number of stored bindings for one watch. A
// repeated register for the same revision must not increase it.
func (d *DB) WatchBindingCount(watchID string) (int, error) {
	if strings.TrimSpace(watchID) == "" {
		return 0, domain.NewAppError(domain.CodeInvalidInput,
			"store: binding: empty watch id")
	}
	ok, err := tableExists(context.Background(), d.readDB, "watch_bindings")
	if err != nil || !ok {
		return 0, err
	}
	var n int
	err = d.readDB.QueryRow(`
		SELECT count(*) FROM watch_bindings WHERE watch_id = ?`, watchID).Scan(&n)
	return n, err
}

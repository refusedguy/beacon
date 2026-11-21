// Package store: project-scoped reads of the executor registry.
//
// One daemon serves several projects from a single process and a single
// database, so every registry read must name the project it belongs to. The
// single-project reads in registry.go and watch_bindings.go take a key in some
// places and omit it in others (WatchBinding and WatchBindingCount are keyed
// by watch id alone). This file adds the missing boundary: a ScopedRegistry is
// bound to one validated ProjectScope at construction, and every read filters
// by project_key in SQL. There is no scope for "all projects", so an explicit
// --project cannot turn a scoped read into a cross-project one (ADR 0086,
// T5.28).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/internal/domain"
)

// ProjectScope is the validated project identity of one registry reader. The
// zero value is unusable: its key is empty. Build a scope with
// NewProjectScope, the only place that accepts a raw key.
type ProjectScope struct {
	key string
}

// NewProjectScope validates a project key and returns the scope used by every
// scoped registry read. The key is the projectStateKey derived from an absolute
// project root, including when that root came from an explicit --project. It
// may never be empty or whitespace: a caller that cannot name a project must
// not silently read every project's rows (ADR 0086).
func NewProjectScope(projectKey string) (ProjectScope, error) {
	key := strings.TrimSpace(projectKey)
	if key == "" {
		return ProjectScope{}, domain.NewAppError(domain.CodeInvalidInput,
			"store: scope: empty project key")
	}
	return ProjectScope{key: key}, nil
}

// Key returns the project key this scope is bound to. It is empty for a zero
// value, which no scoped read accepts.
func (s ProjectScope) Key() string { return s.key }

// requireScope guards a scoped read against a zero or manually built scope.
// It is the last line of defence for callers in this package that construct a
// ScopedRegistry literal instead of using NewScopedRegistry.
func requireScope(s ProjectScope) error {
	if strings.TrimSpace(s.key) == "" {
		return domain.NewAppError(domain.CodeInvalidInput,
			"store: scope: empty project key")
	}
	return nil
}

// ScopedRegistry reads the registry of exactly one project. Construct it with
// NewScopedRegistry; every read then carries that scope's project_key.
type ScopedRegistry struct {
	db    *DB
	scope ProjectScope
}

// NewScopedRegistry binds a registry reader to one project. It refuses an
// empty scope and a nil database rather than returning a reader that could
// query without a project.
func NewScopedRegistry(db *DB, scope ProjectScope) (*ScopedRegistry, error) {
	if err := requireScope(scope); err != nil {
		return nil, err
	}
	if db == nil {
		return nil, domain.NewAppError(domain.CodeInvalidInput,
			"store: scope: nil database")
	}
	// watch_bindings is created by migration 0005 at Open, so the index can be
	// prepared before the first scoped read. Repeating construction is a no-op
	// because the DDL is IF NOT EXISTS.
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			CREATE INDEX IF NOT EXISTS watch_bindings_by_project_watch
			ON watch_bindings(project_key, watch_id, revision DESC)`)
		if err != nil {
			return fmt.Errorf("store: scope: ensure binding index: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return &ScopedRegistry{db: db, scope: scope}, nil
}

// Scope returns the project scope this reader is bound to.
func (r *ScopedRegistry) Scope() ProjectScope { return r.scope }

// scopedExecutorColumns is the shared projection for executor reads. Both
// schema variants (the migration and registry.go's on-demand table) have these
// columns, with capabilities stored as TEXT or BLOB.
const scopedExecutorColumns = `project_key, agent_id, role, session_id, notify_adapter,
       notify_target, capabilities, config_id, status, created_at, updated_at`

// Executor returns one executor of this project by agent id. A row from
// another project is invisible even when the agent id is the same.
func (r *ScopedRegistry) Executor(agentID string) (ExecutorRecord, bool, error) {
	if err := requireScope(r.scope); err != nil {
		return ExecutorRecord{}, false, err
	}
	if strings.TrimSpace(agentID) == "" {
		return ExecutorRecord{}, false, domain.NewAppError(domain.CodeInvalidInput,
			"store: scope: empty agent id")
	}
	ok, err := tableExists(context.Background(), r.db.readDB, "agent_registry")
	if err != nil || !ok {
		return ExecutorRecord{}, false, err
	}
	var e ExecutorRecord
	var caps []byte
	err = r.db.readDB.QueryRow(`SELECT `+scopedExecutorColumns+`
		FROM agent_registry WHERE project_key = ? AND agent_id = ?`,
		r.scope.key, agentID,
	).Scan(&e.ProjectKey, &e.AgentID, &e.Role, &e.SessionID, &e.NotifyAdapter,
		&e.NotifyTarget, &caps, &e.ConfigID, &e.Status, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutorRecord{}, false, nil
	}
	if err != nil {
		return ExecutorRecord{}, false, err
	}
	e.Capabilities = decodeCapabilities(caps)
	return e, true, nil
}

// Executors returns every executor of this project, ordered by agent id. No
// row from another project is included.
func (r *ScopedRegistry) Executors() ([]ExecutorRecord, error) {
	if err := requireScope(r.scope); err != nil {
		return nil, err
	}
	ok, err := tableExists(context.Background(), r.db.readDB, "agent_registry")
	if err != nil || !ok {
		return nil, err
	}
	rows, err := r.db.readDB.Query(`SELECT `+scopedExecutorColumns+`
		FROM agent_registry WHERE project_key = ? ORDER BY agent_id`, r.scope.key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExecutorRecord
	for rows.Next() {
		var e ExecutorRecord
		var caps []byte
		if err := rows.Scan(&e.ProjectKey, &e.AgentID, &e.Role, &e.SessionID,
			&e.NotifyAdapter, &e.NotifyTarget, &caps, &e.ConfigID, &e.Status,
			&e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		e.Capabilities = decodeCapabilities(caps)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ExecutorCount returns the number of executor rows in this project.
func (r *ScopedRegistry) ExecutorCount() (int, error) {
	if err := requireScope(r.scope); err != nil {
		return 0, err
	}
	ok, err := tableExists(context.Background(), r.db.readDB, "agent_registry")
	if err != nil || !ok {
		return 0, err
	}
	var n int
	err = r.db.readDB.QueryRow(`
		SELECT count(*) FROM agent_registry WHERE project_key = ?`,
		r.scope.key).Scan(&n)
	return n, err
}

// scopedWatchBindingColumns is the shared projection for binding reads.
// Migration 0005 stores fencing_token as INTEGER, so this reader and
// readWatchBindingState scan the same stored value without a CAST (BCX-33).
const scopedWatchBindingColumns = `watch_id, revision, project_key, agent_id,
       config_id, fencing_token, created_at`

func scanScopedWatchBinding(scan func(...any) error) (WatchBinding, error) {
	var b WatchBinding
	var revision, token int64
	err := scan(&b.WatchID, &revision, &b.ProjectKey, &b.AgentID,
		&b.ConfigID, &token, &b.CreatedAt)
	if err != nil {
		return WatchBinding{}, err
	}
	b.Revision = uint64(revision)
	b.FencingToken = strconv.FormatInt(token, 10)
	return b, nil
}

// WatchBinding returns the binding for one watch revision in this project.
// The project_key predicate matters because the underlying reads in
// registry.go and watch_bindings.go are keyed by watch id alone: without it a
// caller could follow a watch that belongs to another project.
func (r *ScopedRegistry) WatchBinding(watchID string, revision uint64) (WatchBinding, bool, error) {
	if err := requireScope(r.scope); err != nil {
		return WatchBinding{}, false, err
	}
	if strings.TrimSpace(watchID) == "" {
		return WatchBinding{}, false, domain.NewAppError(domain.CodeInvalidInput,
			"store: scope: empty watch id")
	}
	if revision > uint64(maxSQLiteInt64) {
		return WatchBinding{}, false, domain.NewAppError(domain.CodeInvalidInput,
			"store: scope: revision out of range")
	}
	ok, err := tableExists(context.Background(), r.db.readDB, "watch_bindings")
	if err != nil || !ok {
		return WatchBinding{}, false, err
	}
	row := r.db.readDB.QueryRow(`SELECT `+scopedWatchBindingColumns+`
		FROM watch_bindings
		WHERE project_key = ? AND watch_id = ? AND revision = ?`,
		r.scope.key, watchID, int64(revision))
	b, err := scanScopedWatchBinding(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return WatchBinding{}, false, nil
	}
	if err != nil {
		return WatchBinding{}, false, err
	}
	return b, true, nil
}

// LatestWatchBinding returns the highest stored revision for one watch of
// this project. Older revisions stay in the table for audit but are never
// selected.
func (r *ScopedRegistry) LatestWatchBinding(watchID string) (WatchBinding, bool, error) {
	if err := requireScope(r.scope); err != nil {
		return WatchBinding{}, false, err
	}
	if strings.TrimSpace(watchID) == "" {
		return WatchBinding{}, false, domain.NewAppError(domain.CodeInvalidInput,
			"store: scope: empty watch id")
	}
	ok, err := tableExists(context.Background(), r.db.readDB, "watch_bindings")
	if err != nil || !ok {
		return WatchBinding{}, false, err
	}
	row := r.db.readDB.QueryRow(`SELECT `+scopedWatchBindingColumns+`
		FROM watch_bindings WHERE project_key = ? AND watch_id = ?
		ORDER BY revision DESC LIMIT 1`, r.scope.key, watchID)
	b, err := scanScopedWatchBinding(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return WatchBinding{}, false, nil
	}
	if err != nil {
		return WatchBinding{}, false, err
	}
	return b, true, nil
}

// WatchBindingCount counts the stored revisions of one watch in this project.
// A repeated register for the same revision must not increase it.
func (r *ScopedRegistry) WatchBindingCount(watchID string) (int, error) {
	if err := requireScope(r.scope); err != nil {
		return 0, err
	}
	if strings.TrimSpace(watchID) == "" {
		return 0, domain.NewAppError(domain.CodeInvalidInput,
			"store: scope: empty watch id")
	}
	ok, err := tableExists(context.Background(), r.db.readDB, "watch_bindings")
	if err != nil || !ok {
		return 0, err
	}
	var n int
	err = r.db.readDB.QueryRow(`
		SELECT count(*) FROM watch_bindings
		WHERE project_key = ? AND watch_id = ?`,
		r.scope.key, watchID).Scan(&n)
	return n, err
}

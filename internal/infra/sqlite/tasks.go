package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/refusedguy/beacon/pkg/core"
)

// taskColumns are the epic fields added after the initial tasks table. The
// schema file stays unchanged so existing state databases keep their data.
var taskColumns = []struct{ name, ddl string }{
	{"kind", "kind TEXT NOT NULL DEFAULT 'task'"},
	{"parent", "parent TEXT NOT NULL DEFAULT ''"},
	{"base_branch", "base_branch TEXT NOT NULL DEFAULT ''"},
	{"require_review", "require_review TEXT NOT NULL DEFAULT ''"},
	{"plan_json", "plan_json TEXT NOT NULL DEFAULT '[]'"},
	{"worker_agent", "worker_agent TEXT NOT NULL DEFAULT ''"},
	{"worker_prompt", "worker_prompt TEXT NOT NULL DEFAULT ''"},
}

// ensureTaskColumns adds epic columns to an older tasks table.
func (s *Store) ensureTaskColumns(ctx context.Context) error {
	present, err := presentColumns(ctx, s.db, "tasks")
	if err != nil {
		return err
	}
	for _, col := range taskColumns {
		if !present[col.name] {
			if _, err := s.db.ExecContext(ctx, `ALTER TABLE tasks ADD COLUMN `+col.ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

// syncCache holds the fingerprint of the last successful SyncTasks for one
// database file. Two handles on the same file share the entry, so a sync seen
// by the daemon is also seen by a CLI process in the same process.
type syncCache struct {
	mu          sync.Mutex
	fingerprint []byte
}

var syncCaches sync.Map // map[string]*syncCache

// dbPaths resolves the main file behind each handle once. Store is defined
// outside this card's scope, so it cannot record the path it was opened with;
// PRAGMA database_list is the only way to read it from an open handle.
var dbPaths sync.Map // map[*sql.DB]string

func pathForDB(ctx context.Context, db *sql.DB) (string, error) {
	if v, ok := dbPaths.Load(db); ok {
		return v.(string), nil
	}
	rows, err := db.QueryContext(ctx, `PRAGMA database_list`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	path := ""
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return "", err
		}
		if name == "main" {
			path = file
			break
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if path == "" {
		path = fmt.Sprintf("handle:%p", db)
	}
	actual, _ := dbPaths.LoadOrStore(db, path)
	return actual.(string), nil
}

func cacheForDB(ctx context.Context, db *sql.DB) (*syncCache, error) {
	path, err := pathForDB(ctx, db)
	if err != nil {
		return nil, err
	}
	v, _ := syncCaches.LoadOrStore(path, &syncCache{})
	return v.(*syncCache), nil
}

// syncFingerprint encodes every field SyncTasks persists. core.Task exports
// all of those fields, so the encoding changes whenever a task write would.
// The bytes are held as is, so a warm sync pays no string copy.
func syncFingerprint(tasks []core.Task) ([]byte, error) {
	return json.Marshal(tasks)
}

// syncStatements are the two inserts reused for every card in one sync.
type syncStatements struct {
	task *sql.Stmt
	deps *sql.Stmt
}

func prepareSyncStatements(ctx context.Context, tx *sql.Tx) (*syncStatements, error) {
	task, err := tx.PrepareContext(ctx, `
INSERT INTO tasks(id, title, body, status, priority, scope_json, source_ref, updated_at, kind, parent, base_branch, require_review, plan_json, worker_agent, worker_prompt)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
 title=excluded.title, body=excluded.body, status=excluded.status,
 scope_json=excluded.scope_json, priority=excluded.priority,
 source_ref=excluded.source_ref, updated_at=excluded.updated_at,
 kind=excluded.kind, parent=excluded.parent, base_branch=excluded.base_branch,
 require_review=excluded.require_review, plan_json=excluded.plan_json,
 worker_agent=excluded.worker_agent, worker_prompt=excluded.worker_prompt`)
	if err != nil {
		return nil, err
	}
	deps, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO task_deps(task_id, depends_on) VALUES (?,?)`)
	if err != nil {
		_ = task.Close()
		return nil, err
	}
	return &syncStatements{task: task, deps: deps}, nil
}

func (st *syncStatements) close() {
	_ = st.task.Close()
	_ = st.deps.Close()
}

// beaconDoneTasks returns the ids of stored cards Beacon marked done. A card
// whose status is done in SQLite is authoritative over a stale working tree.
func beaconDoneTasks(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM tasks WHERE status=?`, statusDone)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// writeSyncRows merges the current cards into the stored graph. Beacon controls
// status through SetTaskStatus, so a merge keeps the stored status when it is
// Beacon-authoritative: a task with a live or finished run stays done even if
// a stale working tree still reports it available. Other fields always follow
// the file. Rows and dependencies absent from tasks are removed, so deletions
// still take effect. The statements are prepared once by the caller.
func writeSyncRows(ctx context.Context, tx *sql.Tx, st *syncStatements, tasks []core.Task, done map[string]bool) error {
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		if err := writeSyncTask(ctx, st, t, done); err != nil {
			return err
		}
		ids = append(ids, string(t.ID))
	}
	return pruneSyncRows(ctx, tx, ids)
}

// writeSyncTask upserts one card and its dependency edges. A stale working
// tree cannot revert a card Beacon already marked done.
func writeSyncTask(ctx context.Context, st *syncStatements, t core.Task, done map[string]bool) error {
	scope, err := json.Marshal(t.Scope)
	if err != nil {
		return err
	}
	plan, err := json.Marshal(t.Plan)
	if err != nil {
		return err
	}
	status := string(t.Status)
	if status == "" {
		status = statusAvailable
	}
	if done[string(t.ID)] && status != statusDone && status != statusDropped {
		status = statusDone
	}
	kind := string(t.Kind)
	if kind == "" {
		kind = "task"
	}
	if _, err := st.task.ExecContext(ctx, string(t.ID), t.Title, t.Body, status, t.Priority,
		string(scope), t.SourceRef, nowUnix(), kind, string(t.Parent), t.BaseBranch,
		string(t.RequireReview), string(plan), t.WorkerAgent, t.WorkerPrompt); err != nil {
		return err
	}
	for _, dep := range t.DependsOn {
		if _, err := st.deps.ExecContext(ctx, string(t.ID), string(dep)); err != nil {
			return err
		}
	}
	return nil
}

// pruneSyncRows removes every stored row and edge not named by ids.
func pruneSyncRows(ctx context.Context, tx *sql.Tx, ids []string) error {
	if len(ids) == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_deps`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM tasks`)
		return err
	}
	marks := make([]string, len(ids))
	args := make([]any, 0, 2*len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args = append(args, id)
	}
	depsArgs := append(append([]any(nil), args...), args...)
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_deps WHERE task_id NOT IN (`+strings.Join(marks, ",")+`) OR depends_on NOT IN (`+strings.Join(marks, ",")+`)`, depsArgs...); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE id NOT IN (`+strings.Join(marks, ",")+`)`, args...)
	return err
}

// SyncTasks replaces the stored cards and dependencies with tasks. An
// unchanged fingerprint skips the DELETE and INSERT pairs: the tables already
// hold exactly these cards. SetTaskStatus clears the fingerprint because it
// changes a stored status outside this function.
func (s *Store) SyncTasks(ctx context.Context, tasks []core.Task) error {
	fp, err := syncFingerprint(tasks)
	if err != nil {
		return err
	}
	cache, err := cacheForDB(ctx, s.db)
	if err != nil {
		return err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if bytes.Equal(cache.fingerprint, fp) {
		return nil
	}
	// A failed transaction leaves the cache cold so the next call rewrites the
	// tables instead of trusting a half-applied sync.
	cache.fingerprint = nil

	if err := s.ensureTaskColumns(ctx); err != nil {
		return err
	}
	done, err := beaconDoneTasks(ctx, s.db)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts, err := prepareSyncStatements(ctx, tx)
	if err != nil {
		return err
	}
	defer stmts.close()
	if err := writeSyncRows(ctx, tx, stmts, tasks, done); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	cache.fingerprint = fp
	return nil
}

// readyTasksQuery selects tasks with no unresolved dependencies. Its status
// lists are bound parameters built from the shared definitions, so no caller
// value is interpolated into the SQL.
var readyTasksQuery, readyTasksArgs = inQuery(`
WITH RECURSIVE blocked_chain(task_id, origin, depth) AS (
  SELECT d.task_id, d.depends_on, 1
  FROM task_deps d
  JOIN tasks t ON t.id = d.depends_on
  WHERE t.status NOT IN (%s)
  UNION
  SELECT d.task_id, b.origin, b.depth + 1
  FROM task_deps d
  JOIN blocked_chain b ON d.depends_on = b.task_id
  JOIN tasks t ON t.id = d.depends_on
  WHERE t.status NOT IN (%s) AND b.depth < 64
)
SELECT id, title, body, priority, scope_json, source_ref, kind, parent, base_branch, require_review, plan_json, worker_agent, worker_prompt
FROM tasks
WHERE status IN (%s)
  AND id NOT IN (SELECT task_id FROM blocked_chain)
ORDER BY priority DESC, id`, resolvedTaskStatuses, resolvedTaskStatuses, openTaskStatuses)

// SetTaskStatus sets the status of taskID. A write that matched no row leaves
// the table unchanged, so it keeps the last SyncTasks fingerprint.
func (s *Store) SetTaskStatus(ctx context.Context, taskID core.TaskID, status string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=?, updated_at=? WHERE id=?`, status, nowUnix(), string(taskID))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	path, err := pathForDB(ctx, s.db)
	if err != nil {
		return err
	}
	if v, ok := syncCaches.Load(path); ok {
		cache := v.(*syncCache)
		cache.mu.Lock()
		cache.fingerprint = nil
		cache.mu.Unlock()
	}
	return nil
}

// ListReadyTasks returns the available tasks whose dependencies are resolved.
func (s *Store) ListReadyTasks(ctx context.Context) ([]core.Task, error) {
	if err := s.ensureTaskColumns(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, readyTasksQuery, readyTasksArgs...)
	if err != nil {
		return nil, err
	}
	return collectRows(rows, scanTask)
}

// scanTask decodes one ready task row into a core.Task.
func scanTask(row scanner) (core.Task, error) {
	var t core.Task
	var id, scopeJSON, source, kind, review, planJSON, workerAgent, workerPrompt string
	var base, parent sql.NullString
	if err := row.Scan(&id, &t.Title, &t.Body, &t.Priority, &scopeJSON, &source,
		&kind, &parent, &base, &review, &planJSON, &workerAgent, &workerPrompt); err != nil {
		return t, err
	}
	t.ID = core.TaskID(id)
	t.Status = core.TaskAvailable
	t.SourceRef = source
	t.Kind = core.TaskKind(kind)
	t.Parent = core.TaskID(parent.String)
	t.BaseBranch = base.String
	t.RequireReview = core.ReviewMode(review)
	t.WorkerAgent = workerAgent
	t.WorkerPrompt = workerPrompt
	if err := decodeJSON(scopeJSON, &t.Scope); err != nil {
		return t, err
	}
	if err := decodeJSON(planJSON, &t.Plan); err != nil {
		return t, err
	}
	return t, nil
}

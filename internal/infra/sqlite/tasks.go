package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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
// database. Store is defined outside this card's scope, so the cache is keyed
// by the open *sql.DB. A reopened database starts cold under a new key.
type syncCache struct {
	mu          sync.Mutex
	fingerprint []byte
}

var syncCaches sync.Map // map[*sql.DB]*syncCache

func cacheForDB(db *sql.DB) *syncCache {
	v, _ := syncCaches.LoadOrStore(db, &syncCache{})
	return v.(*syncCache)
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
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
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

// writeSyncRows clears the old graph and inserts the current cards. The
// statements are prepared once by the caller, so each row pays only an Exec.
func writeSyncRows(ctx context.Context, tx *sql.Tx, st *syncStatements, tasks []core.Task) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_deps`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tasks`); err != nil {
		return err
	}
	for _, t := range tasks {
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
	}
	return nil
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
	cache := cacheForDB(s.db)
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
	if err := writeSyncRows(ctx, tx, stmts, tasks); err != nil {
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

// SetTaskStatus sets the status of taskID. The write changes a stored card, so
// the last SyncTasks fingerprint no longer describes the table.
func (s *Store) SetTaskStatus(ctx context.Context, taskID core.TaskID, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=?, updated_at=? WHERE id=?`, status, nowUnix(), string(taskID))
	cache := cacheForDB(s.db)
	cache.mu.Lock()
	cache.fingerprint = nil
	cache.mu.Unlock()
	return err
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

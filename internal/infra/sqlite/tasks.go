package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/refusedguy/beacon/pkg/core"
)

// taskSyncCache holds the fingerprint of the last successful SyncTasks for a
// database. Dispatch re-reads and rewrites the same cards on every cycle, so a
// hit skips two DELETEs and one INSERT per card. The handle is the key because
// two state databases must never share a fingerprint.
var taskSyncCache sync.Map // map[*sql.DB]string

// taskSyncRow is the canonical form hashed by taskFingerprint. updated_at is
// stamped at write time and is deliberately excluded.
type taskSyncRow struct {
	ID            string
	Title         string
	Body          string
	Status        string
	Priority      int
	Scope         string
	SourceRef     string
	Kind          string
	Parent        string
	BaseBranch    string
	RequireReview string
	Plan          string
	WorkerAgent   string
	WorkerPrompt  string
	Depends       []string
}

// taskFingerprint hashes every field SyncTasks writes. Callers skip a rewrite
// only when this value matches the last successful sync for the same database.
func taskFingerprint(tasks []core.Task) (string, error) {
	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, t := range tasks {
		scope, err := json.Marshal(t.Scope)
		if err != nil {
			return "", err
		}
		plan, err := json.Marshal(t.Plan)
		if err != nil {
			return "", err
		}
		status := string(t.Status)
		if status == "" {
			status = statusAvailable
		}
		kind := string(t.Kind)
		if kind == "" {
			kind = "task"
		}
		depends := make([]string, len(t.DependsOn))
		for i, dep := range t.DependsOn {
			depends[i] = string(dep)
		}
		row := taskSyncRow{
			ID: string(t.ID), Title: t.Title, Body: t.Body, Status: status,
			Priority: t.Priority, Scope: string(scope), SourceRef: t.SourceRef,
			Kind: kind, Parent: string(t.Parent), BaseBranch: t.BaseBranch,
			RequireReview: string(t.RequireReview), Plan: string(plan),
			WorkerAgent: t.WorkerAgent, WorkerPrompt: t.WorkerPrompt, Depends: depends,
		}
		if err := enc.Encode(row); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

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

// SyncTasks replaces the stored cards and dependencies with tasks. An
// unchanged source snapshot skips the rewrite; SetTaskStatus and any failed
// transaction clear the cached fingerprint so the next sync starts fresh.
func (s *Store) SyncTasks(ctx context.Context, tasks []core.Task) error {
	if err := s.ensureTaskColumns(ctx); err != nil {
		taskSyncCache.Delete(s.db)
		return err
	}
	fp, err := taskFingerprint(tasks)
	if err != nil {
		taskSyncCache.Delete(s.db)
		return err
	}
	if got, ok := taskSyncCache.Load(s.db); ok && got == fp {
		return nil
	}
	taskSyncCache.Delete(s.db)
	return s.writeTasks(ctx, tasks, fp)
}

// writeTasks commits one full replacement of the task and dependency tables.
func (s *Store) writeTasks(ctx context.Context, tasks []core.Task, fp string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_deps`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tasks`); err != nil {
		return err
	}
	taskInsert, depInsert, err := prepareTaskInserts(ctx, tx)
	if err != nil {
		return err
	}
	defer taskInsert.Close()
	defer depInsert.Close()
	for _, t := range tasks {
		if err := insertTask(ctx, taskInsert, depInsert, t); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	taskSyncCache.Store(s.db, fp)
	return nil
}

// prepareTaskInserts prepares the two INSERT statements once for the whole
// replacement. The caller owns both statements and closes them after the loop.
func prepareTaskInserts(ctx context.Context, tx *sql.Tx) (*sql.Stmt, *sql.Stmt, error) {
	taskInsert, err := tx.PrepareContext(ctx, `
INSERT INTO tasks(id, title, body, status, priority, scope_json, source_ref, updated_at, kind, parent, base_branch, require_review, plan_json, worker_agent, worker_prompt)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return nil, nil, err
	}
	depInsert, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO task_deps(task_id, depends_on) VALUES (?,?)`)
	if err != nil {
		_ = taskInsert.Close()
		return nil, nil, err
	}
	return taskInsert, depInsert, nil
}

// insertTask writes one card and its dependencies through the prepared
// statements, applying the same defaults SyncTasks has always applied.
func insertTask(ctx context.Context, taskInsert, depInsert *sql.Stmt, t core.Task) error {
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
	if _, err := taskInsert.ExecContext(ctx, string(t.ID), t.Title, t.Body, status, t.Priority, string(scope), t.SourceRef, nowUnix(),
		kind, string(t.Parent), t.BaseBranch, string(t.RequireReview), string(plan), t.WorkerAgent, t.WorkerPrompt); err != nil {
		return err
	}
	for _, dep := range t.DependsOn {
		if _, err := depInsert.ExecContext(ctx, string(t.ID), string(dep)); err != nil {
			return err
		}
	}
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

// SetTaskStatus sets the status of taskID. A local claim is not reflected in
// the source cards, so the next SyncTasks must rewrite from source instead of
// trusting the cached fingerprint.
func (s *Store) SetTaskStatus(ctx context.Context, taskID core.TaskID, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=?, updated_at=? WHERE id=?`, status, nowUnix(), string(taskID))
	taskSyncCache.Delete(s.db)
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

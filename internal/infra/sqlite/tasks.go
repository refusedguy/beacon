package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

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

// SyncTasks replaces the stored cards and dependencies with tasks.
func (s *Store) SyncTasks(ctx context.Context, tasks []core.Task) error {
	if err := s.ensureTaskColumns(ctx); err != nil {
		return err
	}
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
		if _, err := tx.ExecContext(ctx, `
INSERT INTO tasks(id, title, body, status, priority, scope_json, source_ref, updated_at, kind, parent, base_branch, require_review, plan_json, worker_agent, worker_prompt)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, string(t.ID), t.Title, t.Body, status, t.Priority, string(scope), t.SourceRef, nowUnix(),
			kind, string(t.Parent), t.BaseBranch, string(t.RequireReview), string(plan), t.WorkerAgent, t.WorkerPrompt); err != nil {
			return err
		}
		for _, dep := range t.DependsOn {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO task_deps(task_id, depends_on) VALUES (?,?)`, string(t.ID), string(dep)); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
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

// SetTaskStatus sets the status of taskID.
func (s *Store) SetTaskStatus(ctx context.Context, taskID core.TaskID, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=?, updated_at=? WHERE id=?`, status, nowUnix(), string(taskID))
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

package sqlite

import (
	"context"
	"encoding/json"

	"github.com/refusedguy/beacon/pkg/core"
)

func (s *Store) SyncTasks(ctx context.Context, tasks []core.Task) error {
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
		status := string(t.Status)
		if status == "" {
			status = "available"
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO tasks(id, title, body, status, priority, scope_json, source_ref, updated_at)
VALUES (?,?,?,?,?,?,?,?)`, string(t.ID), t.Title, t.Body, status, t.Priority, string(scope), t.SourceRef, nowUnix()); err != nil {
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

const readyTasksQuery = `
WITH RECURSIVE blocked_chain(task_id, origin, depth) AS (
  SELECT d.task_id, d.depends_on, 1
  FROM task_deps d
  JOIN tasks t ON t.id = d.depends_on
  WHERE t.status NOT IN ('done', 'dropped')
  UNION
  SELECT d.task_id, b.origin, b.depth + 1
  FROM task_deps d
  JOIN blocked_chain b ON d.depends_on = b.task_id
  JOIN tasks t ON t.id = d.depends_on
  WHERE t.status NOT IN ('done', 'dropped') AND b.depth < 64
)
SELECT id, title, body, priority, scope_json, source_ref
FROM tasks
WHERE status IN ('available', 'todo')
  AND id NOT IN (SELECT task_id FROM blocked_chain)
ORDER BY priority DESC, id`

func (s *Store) SetTaskStatus(ctx context.Context, taskID core.TaskID, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=?, updated_at=? WHERE id=?`, status, nowUnix(), string(taskID))
	return err
}

func (s *Store) ListReadyTasks(ctx context.Context) ([]core.Task, error) {
	rows, err := s.db.QueryContext(ctx, readyTasksQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Task
	for rows.Next() {
		var t core.Task
		var id, scopeJSON string
		var source string
		if err := rows.Scan(&id, &t.Title, &t.Body, &t.Priority, &scopeJSON, &source); err != nil {
			return nil, err
		}
		t.ID = core.TaskID(id)
		t.Status = core.TaskAvailable
		t.SourceRef = source
		if err := json.Unmarshal([]byte(scopeJSON), &t.Scope); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

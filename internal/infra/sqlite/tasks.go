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
	{"base_branch", "base_branch TEXT NOT NULL DEFAULT ''"},
	{"require_review", "require_review TEXT NOT NULL DEFAULT ''"},
	{"plan_json", "plan_json TEXT NOT NULL DEFAULT '[]'"},
}

func (s *Store) ensureTaskColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(tasks)`)
	if err != nil {
		return err
	}
	present := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		present[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
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
			status = "available"
		}
		kind := string(t.Kind)
		if kind == "" {
			kind = "task"
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO tasks(id, title, body, status, priority, scope_json, source_ref, updated_at, kind, base_branch, require_review, plan_json)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, string(t.ID), t.Title, t.Body, status, t.Priority, string(scope), t.SourceRef, nowUnix(),
			kind, t.BaseBranch, string(t.RequireReview), string(plan)); err != nil {
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
SELECT id, title, body, priority, scope_json, source_ref, kind, base_branch, require_review, plan_json
FROM tasks
WHERE status IN ('available', 'todo')
  AND id NOT IN (SELECT task_id FROM blocked_chain)
ORDER BY priority DESC, id`

func (s *Store) SetTaskStatus(ctx context.Context, taskID core.TaskID, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=?, updated_at=? WHERE id=?`, status, nowUnix(), string(taskID))
	return err
}

func (s *Store) ListReadyTasks(ctx context.Context) ([]core.Task, error) {
	if err := s.ensureTaskColumns(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, readyTasksQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Task
	for rows.Next() {
		var t core.Task
		var id, scopeJSON, source, kind, review, planJSON string
		var base sql.NullString
		if err := rows.Scan(&id, &t.Title, &t.Body, &t.Priority, &scopeJSON, &source,
			&kind, &base, &review, &planJSON); err != nil {
			return nil, err
		}
		t.ID = core.TaskID(id)
		t.Status = core.TaskAvailable
		t.SourceRef = source
		t.Kind = core.TaskKind(kind)
		t.BaseBranch = base.String
		t.RequireReview = core.ReviewMode(review)
		if err := json.Unmarshal([]byte(scopeJSON), &t.Scope); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(planJSON), &t.Plan); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

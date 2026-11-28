package sqlite

import (
	"context"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// ListQueueTasks reads local task cards and their unresolved dependencies
// straight from SQLite. It makes no SCM or task-source calls, so a snapshot
// stays safe for both markdown and remote task sources.
func (s *Store) ListQueueTasks(ctx context.Context) ([]app.QueueTask, error) {
	if err := s.ensureTaskColumns(ctx); err != nil {
		return nil, err
	}
	queueQuery, queueArgs := inQuery(`SELECT id, title, priority FROM tasks
WHERE status IN (%s)
  AND id NOT IN (SELECT task_id FROM runs WHERE state NOT IN (%s))
ORDER BY priority DESC, id`, openTaskStatuses, terminalStates)
	rows, err := s.db.QueryContext(ctx, queueQuery, queueArgs...)
	if err != nil {
		return nil, err
	}
	queue := []app.QueueTask{}
	byID := make(map[string]int)
	for rows.Next() {
		var task app.QueueTask
		if err := rows.Scan(&task.ID, &task.Title, &task.Priority); err != nil {
			rows.Close()
			return nil, err
		}
		task.DependsOn = []string{}
		queue = append(queue, task)
		byID[task.ID] = len(queue) - 1
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Use the same dependency predicate as readyTasksQuery. A dangling edge
	// names a deleted card, so it is gone rather than pending. SyncTasks logs
	// the edge before pruning it, which gives the verdict a clear reason.
	depsQuery, depsArgs := inQuery(`SELECT d.task_id, d.depends_on `+unresolvedDependencies+`
ORDER BY d.task_id, d.depends_on`, resolvedTaskStatuses)
	deps, err := s.db.QueryContext(ctx, depsQuery, depsArgs...)
	if err != nil {
		return nil, err
	}
	defer deps.Close()
	for deps.Next() {
		var taskID, dependsOn string
		if err := deps.Scan(&taskID, &dependsOn); err != nil {
			return nil, err
		}
		if i, ok := byID[taskID]; ok {
			queue[i].DependsOn = append(queue[i].DependsOn, dependsOn)
			queue[i].Waiting = true
		}
	}
	return queue, deps.Err()
}

// CurrentIncident returns the newest open base-branch incident, if any.
func (s *Store) CurrentIncident(ctx context.Context) (*core.BaseIncident, error) {
	return s.scanIncident(s.db.QueryRowContext(ctx,
		`SELECT `+incidentColumns+` FROM base_incidents WHERE status='open' ORDER BY created_at DESC, id DESC LIMIT 1`))
}

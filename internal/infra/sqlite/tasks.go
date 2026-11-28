package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
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

// validateUniqueTaskIDs rejects a graph that names one ID twice. SQLite stores
// one row per ID through the tasks primary key, so a duplicate would make the
// index's first-path-wins answer disagree with the row written last.
func validateUniqueTaskIDs(tasks []core.Task) error {
	seen := make(map[core.TaskID]bool, len(tasks))
	for _, t := range tasks {
		if seen[t.ID] {
			return core.Errorf(core.CodeInvalidInput, "duplicate task id %q", t.ID)
		}
		seen[t.ID] = true
	}
	return nil
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

// beaconOwnedStatuses returns the status Beacon wrote for each stored card.
// A claimed or done status in SQLite is authoritative over a stale working
// tree: the worker changes the card in its own worktree, so main can still
// report the older status while a run is in flight.
func beaconOwnedStatuses(ctx context.Context, db *sql.DB) (map[string]string, error) {
	query, args := inQuery(`SELECT id, status FROM tasks WHERE status IN (%s)`,
		[]string{statusDone, string(core.TaskClaimed)})
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, err
		}
		out[id] = status
	}
	return out, rows.Err()
}

// mergeSyncStatus keeps a Beacon-written status when the file still reports an
// unclaimed status. A file that already reports claimed, done or dropped is
// newer than the stored row and wins. Other statuses follow the file.
func mergeSyncStatus(fileStatus, owned string) string {
	status := fileStatus
	if status == "" {
		status = statusAvailable
	}
	switch owned {
	case statusDone:
		if status != statusDone && status != statusDropped {
			return statusDone
		}
	case string(core.TaskClaimed):
		if status == statusAvailable || status == statusTodo {
			return string(core.TaskClaimed)
		}
	}
	return status
}

// writeSyncRows merges the current cards into the stored graph. Beacon controls
// status through SetTaskStatus, so a merge keeps the stored status when it is
// Beacon-authoritative: a task with a live or finished run stays done even if
// a stale working tree still reports it available. Other fields always follow
// the file. Beacon-owned rows are also kept by the prune, so a card absent from
// tasks is not deleted and later reinserted as available. Rows and dependencies
// absent from tasks and not Beacon-owned are removed, so deletions still take
// effect. The statements are prepared once by the caller.
func writeSyncRows(ctx context.Context, tx *sql.Tx, st *syncStatements, tasks []core.Task, owned map[string]string) error {
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		if err := writeSyncTask(ctx, st, t, owned[string(t.ID)]); err != nil {
			return err
		}
		ids = append(ids, string(t.ID))
	}
	return pruneSyncRows(ctx, tx, ids, owned)
}

// writeSyncTask upserts one card and its dependency edges. A stale working
// tree cannot revert a status Beacon already wrote to the stored card.
func writeSyncTask(ctx context.Context, st *syncStatements, t core.Task, owned string) error {
	scope, err := json.Marshal(t.Scope)
	if err != nil {
		return err
	}
	plan, err := json.Marshal(t.Plan)
	if err != nil {
		return err
	}
	status := mergeSyncStatus(string(t.Status), owned)
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

// logDanglingEdges names an edge whose target card is no longer in the graph
// before pruning removes it. The log gives an operator a clear reason why the
// dependency stopped blocking the task in both readers.
func logDanglingEdges(ctx context.Context, tx *sql.Tx, ids []string) error {
	query := `SELECT task_id, depends_on FROM task_deps`
	var args []any
	if len(ids) > 0 {
		q, a := inQuery(`SELECT task_id, depends_on FROM task_deps WHERE depends_on NOT IN (%s)`, ids)
		query, args = q, a
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var task, dep string
		if err := rows.Scan(&task, &dep); err != nil {
			return err
		}
		slog.Warn("sqlite: dropping dependency on missing card", "task", task, "depends_on", dep)
	}
	return rows.Err()
}

// pruneSyncRows removes every stored row and edge whose card is neither in ids
// nor Beacon-owned. A done or claimed row written by SetTaskStatus stays, so a
// sync whose source no longer lists that card cannot delete it and let a stale
// available frontmatter reinsert it as ready.
func pruneSyncRows(ctx context.Context, tx *sql.Tx, ids []string, owned map[string]string) error {
	keep := append([]string(nil), ids...)
	seen := make(map[string]bool, len(keep)+len(owned))
	for _, id := range keep {
		seen[id] = true
	}
	for id := range owned {
		if !seen[id] {
			seen[id] = true
			keep = append(keep, id)
		}
	}
	if len(keep) == 0 {
		if err := logDanglingEdges(ctx, tx, nil); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_deps`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM tasks`)
		return err
	}
	marks := make([]string, len(keep))
	args := make([]any, 0, 2*len(keep))
	for i, id := range keep {
		marks[i] = "?"
		args = append(args, id)
	}
	if err := logDanglingEdges(ctx, tx, keep); err != nil {
		return err
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
	// Reject an impossible graph before touching the cache or the tables, so a
	// failed sync leaves both the stored graph and the fingerprint unchanged.
	if err := core.ValidateTaskGraph(tasks); err != nil {
		return err
	}
	if err := validateUniqueTaskIDs(tasks); err != nil {
		return err
	}
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
	owned, err := beaconOwnedStatuses(ctx, s.db)
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
	if err := writeSyncRows(ctx, tx, stmts, tasks, owned); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	cache.fingerprint = fp
	return nil
}

// unresolvedDependencies is the one predicate that decides whether a
// dependency still blocks a task. An INNER JOIN drops an edge whose target
// card was deleted, so a dangling dependency is treated as gone by both the
// dispatcher and the monitor. SyncTasks logs the edge before it prunes it,
// which gives that decision a clear reason in the daemon log. A task blocked
// through a chain is already blocked by its direct dependency, so this
// one-hop check covers the same set without walking the graph to a depth
// limit. Status lists are bound parameters built from the shared definitions.
const unresolvedDependencies = `FROM task_deps d
    JOIN tasks dep ON dep.id = d.depends_on
    WHERE dep.status NOT IN (%s)`

// readyTasksQuery selects tasks with no unresolved dependency. It shares the
// dependency predicate with ListQueueTasks so their verdicts cannot drift.
var readyTasksQuery, readyTasksArgs = inQuery(`
SELECT id, title, body, priority, scope_json, source_ref, kind, parent, base_branch, require_review, plan_json, worker_agent, worker_prompt
FROM tasks t
WHERE t.status IN (%s)
  AND NOT EXISTS (
    SELECT 1 `+unresolvedDependencies+`
    AND d.task_id = t.id
  )
ORDER BY priority DESC, id`, openTaskStatuses, resolvedTaskStatuses)

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

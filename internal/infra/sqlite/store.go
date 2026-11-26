// Package sqlite is the single RunStore implementation.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// Store is the SQLite implementation of the run and decision stores.
type Store struct{ db *sql.DB }

// Open opens the SQLite state database at path.
func Open(path string) (*Store, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	if err := ensureSnapshotIndexes(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureDecisionSummary(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// ensureSnapshotIndexes adds the covering indexes the snapshot reads rely on.
// They live on the read side, next to the queries they serve, so an older
// state database gains them on the next open without a schema version bump.
func ensureSnapshotIndexes(db *sql.DB) error {
	for _, ddl := range []string{
		`CREATE INDEX IF NOT EXISTS runs_state_updated_idx ON runs(state, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS decisions_run_kind_verdict_action_idx ON decisions(run_id, kind, verdict, action)`,
		`CREATE INDEX IF NOT EXISTS decisions_orphan_at_idx ON decisions(run_id, at DESC) WHERE run_id = ''`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			return err
		}
	}
	return nil
}

// ensureDecisionSummary keeps a small aggregate of decision counts so a
// snapshot never scans the decision table to build its metrics. A trigger
// maintains the aggregate on every insert; the backfill makes an older state
// database correct on the next open.
func ensureDecisionSummary(db *sql.DB) error {
	const table = `CREATE TABLE IF NOT EXISTS decision_summary (
run_id TEXT NOT NULL, kind TEXT NOT NULL, verdict TEXT NOT NULL, action TEXT NOT NULL,
n INTEGER NOT NULL, PRIMARY KEY (run_id, kind, verdict, action))`
	if _, err := db.Exec(table); err != nil {
		return err
	}
	const trigger = `CREATE TRIGGER IF NOT EXISTS decision_summary_insert
AFTER INSERT ON decisions BEGIN
INSERT INTO decision_summary (run_id, kind, verdict, action, n)
VALUES (NEW.run_id, NEW.kind, NEW.verdict, NEW.action, 1)
ON CONFLICT (run_id, kind, verdict, action) DO UPDATE SET n = n + 1;
END`
	if _, err := db.Exec(trigger); err != nil {
		return err
	}
	// Backfill only when the aggregate is empty. A populated summary is kept
	// current by the trigger, so a restart must not regroup a large decision
	// table for no reason.
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM decision_summary`).Scan(&rows); err != nil {
		return err
	}
	if rows > 0 {
		return nil
	}
	_, err := db.Exec(`INSERT INTO decision_summary (run_id, kind, verdict, action, n)
SELECT run_id, kind, verdict, action, COUNT(*) FROM decisions
GROUP BY run_id, kind, verdict, action
ON CONFLICT (run_id, kind, verdict, action) DO UPDATE SET n = excluded.n`)
	return err
}

// Close closes the database handle.
func (s *Store) Close() error { return s.db.Close() }

// runColumns lists the run fields stored in one row.
const runColumns = `id, task_id, state, pr_number, head_sha, observed_sha, observed_base_sha, session_id,
 parent_session_id, workspace_path, branch, scope_json, blocked_incident_id, is_fixer,
 rebase_attempts, fix_attempts, max_attempts, reason, checks_json, created_at, updated_at`

// Save inserts or updates r and records a timeline event when the state changes.
func (s *Store) Save(ctx context.Context, r *core.Run) error {
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now
	}
	scope, err := json.Marshal(r.Scope)
	if err != nil {
		return err
	}
	checks, err := json.Marshal(checksForSave(r.Checks))
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var previous string
	newRun := false
	err = tx.QueryRowContext(ctx, `SELECT state FROM runs WHERE id=?`, string(r.ID)).Scan(&previous)
	switch {
	case err == nil:
	case err == sql.ErrNoRows:
		newRun = true
	default:
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO runs (`+runColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
 state=excluded.state, pr_number=excluded.pr_number, head_sha=excluded.head_sha,
 observed_sha=excluded.observed_sha, observed_base_sha=excluded.observed_base_sha,
 session_id=excluded.session_id,
 parent_session_id=excluded.parent_session_id, workspace_path=excluded.workspace_path,
 branch=excluded.branch, scope_json=excluded.scope_json,
 blocked_incident_id=excluded.blocked_incident_id, is_fixer=excluded.is_fixer,
 rebase_attempts=excluded.rebase_attempts, fix_attempts=excluded.fix_attempts,
 max_attempts=excluded.max_attempts, reason=excluded.reason,
 checks_json=excluded.checks_json, updated_at=excluded.updated_at`,
		string(r.ID), string(r.TaskID), string(r.State), r.PRNumber, r.HeadSHA,
		r.ObservedSHA, r.ObservedBaseSHA, r.SessionID, r.ParentSessionID, r.WorkspacePath, r.Branch,
		string(scope), r.BlockedIncidentID, boolInt(r.IsFixer), r.RebaseAttempts,
		r.FixAttempts, r.MaxAttempts, r.Reason, string(checks), r.CreatedAt.UnixNano(), r.UpdatedAt.UnixNano())
	if err != nil {
		return err
	}
	if newRun || previous != string(r.State) {
		kind, state, at := stateEventKind(r.State), r.State, r.UpdatedAt
		if newRun {
			at = r.CreatedAt
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO run_events (run_id, kind, state, at, head_sha, pr_number, reason) VALUES (?,?,?,?,?,?,?)`,
			string(r.ID), kind, string(state), at.UnixNano(), r.HeadSHA, r.PRNumber, r.Reason)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// stateEventKind names the timeline entry recorded when a run enters a state.
func stateEventKind(state core.RunState) string {
	switch state {
	case core.RunPending:
		return "start"
	case core.RunRunning:
		return "started"
	case core.RunWaitingCI:
		return "checks_pending"
	case core.RunNeedsRebase:
		return "behind"
	case core.RunNeedsAgentFix:
		return "checks_failed"
	case core.RunBlocked:
		return "blocked"
	case core.RunMerged:
		return "merged"
	case core.RunClosed:
		return "closed"
	default:
		return string(state)
	}
}

// boolInt converts b to an integer for SQLite storage.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// checksForSave keeps checks_json an array even when a run was never observed.
// The UI reads run.checks directly and expects [] for older runs.
func checksForSave(checks []core.CheckResult) []core.CheckResult {
	if checks == nil {
		return []core.CheckResult{}
	}
	return checks
}

// Get returns the run with id, or nil when no run has that id.
func (s *Store) Get(ctx context.Context, id core.RunID) (*core.Run, error) {
	return s.queryOne(ctx, `SELECT `+runColumns+` FROM runs WHERE id=?`, string(id))
}

// FindByPR returns the newest active run bound to pull request pr.
func (s *Store) FindByPR(ctx context.Context, pr int) (*core.Run, error) {
	return s.queryOne(ctx, `SELECT `+runColumns+` FROM runs WHERE pr_number=? AND state NOT IN ('merged','closed') ORDER BY updated_at DESC LIMIT 1`, pr)
}

// FindActiveByTask returns the newest nonterminal run for task.
func (s *Store) FindActiveByTask(ctx context.Context, task core.TaskID) (*core.Run, error) {
	return s.queryOne(ctx, `SELECT `+runColumns+` FROM runs WHERE task_id=? AND state NOT IN ('merged','closed','dropped') ORDER BY updated_at DESC LIMIT 1`, string(task))
}

// ActiveTaskIDs returns the state of one nonterminal run for each requested
// task. Tasks without a run in flight are absent from the result. The query
// reads only task_id and state, so callers that just need to know whether a
// task is busy do not decode full runs and their JSON columns.
func (s *Store) ActiveTaskIDs(ctx context.Context, taskIDs []string) (map[string]core.RunState, error) {
	out := make(map[string]core.RunState)
	if len(taskIDs) == 0 {
		return out, nil
	}
	query, args := inQuery(`SELECT task_id, state FROM runs WHERE task_id IN (%s)
AND state NOT IN ('merged','closed','dropped')`, taskIDs)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var task, state string
		if err := rows.Scan(&task, &state); err != nil {
			return nil, err
		}
		out[task] = core.RunState(state)
	}
	return out, rows.Err()
}

// deliveryRetention bounds how long delivery keys and approvals stay in the
// state database. Both are idempotency markers for a specific SHA, so a row
// older than this window can never affect a new decision.
const deliveryRetention = 30 * 24 * time.Hour

// retentionBatchSize caps how many rows one prune pass removes from a table.
// The reconcile tick stays short even after a long outage with accumulated
// rows; a later tick removes the next batch.
const retentionBatchSize = 1000

// PruneStale deletes delivery and approval rows older than retention. It
// returns the number of rows removed. A non-positive retention uses the
// default 30-day window.
func (s *Store) PruneStale(ctx context.Context, retention time.Duration) (int64, error) {
	if retention <= 0 {
		retention = deliveryRetention
	}
	cutoff := nowUnix() - retention.Nanoseconds()
	queries := []string{
		`DELETE FROM deliveries WHERE key IN (SELECT key FROM deliveries WHERE created_at < ? LIMIT ?)`,
		`DELETE FROM approvals WHERE rowid IN (SELECT rowid FROM approvals WHERE created_at < ? LIMIT ?)`,
	}
	var removed int64
	for _, query := range queries {
		res, err := s.db.ExecContext(ctx, query, cutoff, retentionBatchSize)
		if err != nil {
			return removed, err
		}
		n, err := res.RowsAffected()
		removed += n
		if err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// ListActive returns every run that is not merged or closed.
func (s *Store) ListActive(ctx context.Context) ([]*core.Run, error) {
	// The reconciler calls ListActive on every tick. Reclaiming old rows here
	// puts retention on that fixed schedule without a second goroutine or a
	// second timer in the daemon. Cleanup is best effort: a failed prune must
	// not stop the tick from reconciling active runs.
	if _, err := s.PruneStale(ctx, deliveryRetention); err != nil {
		slog.Warn("prune stale delivery rows", "error", err)
	}
	return s.queryAll(ctx, `SELECT `+runColumns+` FROM runs WHERE state NOT IN ('merged','closed') ORDER BY updated_at`)
}

// ListRecent returns at most limit finished runs, newest first.
func (s *Store) ListRecent(ctx context.Context, limit int) ([]*core.Run, error) {
	if limit <= 0 {
		limit = app.DefaultRecentLimit
	}
	// Each terminal state is already ordered by updated_at in the covering
	// index. Taking limit rows from each state and merging them avoids sorting
	// every finished run in the database. The overall newest limit rows are
	// always inside that three-way union, so the result is unchanged.
	n := strconv.Itoa(limit)
	terminal := func(state string) string {
		return `SELECT * FROM (SELECT ` + runColumns + ` FROM runs WHERE state='` + state + `'
ORDER BY updated_at DESC LIMIT ` + n + `)`
	}
	query := `SELECT ` + runColumns + ` FROM (
` + terminal("merged") + `
UNION ALL
` + terminal("closed") + `
UNION ALL
` + terminal("dropped") + `
) ORDER BY updated_at DESC LIMIT ` + n
	return s.queryAll(ctx, query)
}

// ListByIncident returns the runs blocked by incidentID.
func (s *Store) ListByIncident(ctx context.Context, incidentID string) ([]*core.Run, error) {
	return s.queryAll(ctx, `SELECT `+runColumns+` FROM runs WHERE blocked_incident_id=? AND state='blocked'`, incidentID)
}

// queryAll runs query and scans every returned row into a run.
func (s *Store) queryAll(ctx context.Context, query string, args ...any) ([]*core.Run, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*core.Run, 0)
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// queryOne runs query and scans at most one row into a run.
func (s *Store) queryOne(ctx context.Context, query string, args ...any) (*core.Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, query, args...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// scanner is the row interface shared by QueryRow and Rows.
type scanner interface{ Scan(...any) error }

// scanRun decodes one database row into a core.Run.
func scanRun(row scanner) (*core.Run, error) {
	var r core.Run
	var id, task, state, scopeJSON, checksJSON string
	var created, updated int64
	var fixer int
	err := row.Scan(&id, &task, &state, &r.PRNumber, &r.HeadSHA, &r.ObservedSHA,
		&r.ObservedBaseSHA, &r.SessionID, &r.ParentSessionID, &r.WorkspacePath, &r.Branch, &scopeJSON,
		&r.BlockedIncidentID, &fixer, &r.RebaseAttempts, &r.FixAttempts,
		&r.MaxAttempts, &r.Reason, &checksJSON, &created, &updated)
	if err != nil {
		return nil, err
	}
	r.ID = core.RunID(id)
	r.TaskID = core.TaskID(task)
	r.State = core.RunState(state)
	r.IsFixer = fixer != 0
	if err := json.Unmarshal([]byte(scopeJSON), &r.Scope); err != nil {
		return nil, fmt.Errorf("scope_json: %w", err)
	}
	if err := json.Unmarshal([]byte(checksJSON), &r.Checks); err != nil {
		return nil, fmt.Errorf("checks_json: %w", err)
	}
	r.Checks = checksForSave(r.Checks)
	r.CreatedAt = time.Unix(0, created).UTC()
	r.UpdatedAt = time.Unix(0, updated).UTC()
	return &r, nil
}

// ClaimDelivery records key and reports whether this call inserted it.
func (s *Store) ClaimDelivery(ctx context.Context, key string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO deliveries(key, created_at) VALUES (?, ?)`, key, nowUnix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReleaseDelivery removes key so it can be claimed again.
func (s *Store) ReleaseDelivery(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM deliveries WHERE key=?`, key)
	return err
}

// SetApproval records an approval for run id at sha.
func (s *Store) SetApproval(ctx context.Context, id core.RunID, sha string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO approvals(run_id, sha, created_at) VALUES (?,?,?)`, string(id), sha, nowUnix())
	return err
}

// Approved reports whether run id has an approval for sha.
func (s *Store) Approved(ctx context.Context, id core.RunID, sha string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE run_id=? AND sha=?`, string(id), sha).Scan(&n)
	return n > 0, err
}

// RecordDecision implements app.DecisionSink.
func (s *Store) RecordDecision(ctx context.Context, d app.Decision) error {
	if d.At.IsZero() {
		d.At = time.Now().UTC()
	}
	fields := []byte("{}")
	if d.Fields != nil {
		var err error
		fields, err = json.Marshal(d.Fields)
		if err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO decisions (at, cycle_id, kind, run_id, task_id, pr_number, verdict, action, reason, fields_json)
VALUES (?,?,?,?,?,?,?,?,?,?)`,
		d.At.UnixNano(), d.CycleID, d.Kind, d.RunID, d.TaskID, d.PRNumber,
		d.Verdict, d.Action, d.Reason, string(fields))
	return err
}

// ListRunEvents returns the timelines of all given runs in one query.
func (s *Store) ListRunEvents(ctx context.Context, runIDs []string) ([]app.RunEvent, error) {
	out := []app.RunEvent{}
	if len(runIDs) == 0 {
		return out, nil
	}
	query, args := inQuery(`SELECT id, run_id, kind, state, at, head_sha, pr_number, reason
FROM run_events WHERE run_id IN (%s) ORDER BY at, id`, runIDs)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var event app.RunEvent
		var at int64
		if err := rows.Scan(&event.ID, &event.RunID, &event.Kind, &event.State,
			&at, &event.HeadSHA, &event.PRNumber, &event.Reason); err != nil {
			return nil, err
		}
		event.At = time.Unix(0, at).UTC()
		out = append(out, event)
	}
	return out, rows.Err()
}

// decisionColumns lists the decision fields stored in one row.
const decisionColumns = `id, at, cycle_id, kind, run_id, task_id, pr_number, verdict, action, reason, fields_json`

// ListDecisions returns a bounded decision history in one query. Each run keeps
// only its newest decisions; run-less decisions are capped separately so a long
// dispatch history cannot inflate a snapshot. Totals come from DecisionCounts.
func (s *Store) ListDecisions(ctx context.Context, runIDs []string, limit, orphanLimit int) ([]app.Decision, error) {
	out := []app.Decision{}
	if limit <= 0 {
		limit = app.DefaultDecisionsLimit
	}
	if orphanLimit <= 0 {
		orphanLimit = app.DefaultOrphanDecisionsLimit
	}
	// The orphan bucket can hold hundreds of thousands of rows. A window
	// function numbers every one of them before the LIMIT is applied. Because
	// the caller only wants the newest orphanLimit rows, an ordered index scan
	// with LIMIT returns the same rows and skips the full sort. The limit is
	// inlined because modernc/sqlite rejects a bound parameter in this LIMIT.
	orphan := `SELECT ` + decisionColumns + ` FROM decisions
WHERE run_id = '' ORDER BY at DESC, id DESC LIMIT ` + strconv.Itoa(orphanLimit)
	query := `WITH orphan AS (` + orphan + `)
SELECT ` + decisionColumns + ` FROM orphan ORDER BY at, id`
	var args []any
	if len(runIDs) > 0 {
		perRun, ids := inQuery(`per_run AS (SELECT `+decisionColumns+`,
ROW_NUMBER() OVER (PARTITION BY run_id ORDER BY at DESC, id DESC) AS rn
FROM decisions WHERE run_id IN (%s))`, runIDs)
		query = `WITH ` + perRun + `, orphan AS (` + orphan + `)
SELECT ` + decisionColumns + ` FROM per_run WHERE rn <= ?
UNION ALL SELECT ` + decisionColumns + ` FROM orphan
ORDER BY at, id`
		args = append(ids, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// scanDecision decodes one decision row into an app.Decision. Row-level errors,
// including sql.ErrNoRows, are returned to the caller to handle.
func scanDecision(row scanner) (*app.Decision, error) {
	var d app.Decision
	var at int64
	var fields string
	if err := row.Scan(&d.ID, &at, &d.CycleID, &d.Kind, &d.RunID, &d.TaskID,
		&d.PRNumber, &d.Verdict, &d.Action, &d.Reason, &fields); err != nil {
		return nil, err
	}
	d.At = time.Unix(0, at).UTC()
	if err := json.Unmarshal([]byte(fields), &d.Fields); err != nil {
		return nil, fmt.Errorf("fields_json: %w", err)
	}
	return &d, nil
}

// DecisionCounts aggregates every decision into a handful of rows. It keeps
// snapshot metrics exact even though ListDecisions returns a bounded sample.
func (s *Store) DecisionCounts(ctx context.Context, runIDs []string) ([]app.DecisionCount, error) {
	out := []app.DecisionCount{}
	// Read the maintained aggregate instead of scanning decisions. The two
	// buckets are read separately so both use the primary key on run_id, and
	// the outer GROUP BY adds overlapping groups exactly as before.
	const orphan = `SELECT kind, verdict, action, n FROM decision_summary WHERE run_id = ''`
	query := `SELECT kind, verdict, action, n FROM (` + orphan + `) ORDER BY kind, verdict, action`
	var args []any
	if len(runIDs) > 0 {
		runs, ids := inQuery(`SELECT kind, verdict, action, n FROM decision_summary
WHERE run_id IN (%s)`, runIDs)
		query = `SELECT kind, verdict, action, SUM(n) AS n FROM (` + runs + ` UNION ALL ` + orphan + `)
GROUP BY kind, verdict, action ORDER BY kind, verdict, action`
		args = ids
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c app.DecisionCount
		if err := rows.Scan(&c.Kind, &c.Verdict, &c.Action, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LastDecision returns the newest stored decision for a run. It implements
// app.DecisionLookup so unchanged reconcile decisions can be skipped.
func (s *Store) LastDecision(ctx context.Context, runID string) (*app.Decision, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+decisionColumns+`
FROM decisions WHERE run_id=? ORDER BY at DESC, id DESC LIMIT 1`, runID)
	d, err := scanDecision(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return d, err
}

// ListQueueTasks reads local task cards and their unresolved dependencies
// straight from SQLite. It makes no SCM or task-source calls, so a snapshot
// stays safe for both markdown and remote task sources.
func (s *Store) ListQueueTasks(ctx context.Context) ([]app.QueueTask, error) {
	if err := s.ensureTaskColumns(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, title, priority FROM tasks
WHERE status IN ('available','todo')
  AND id NOT IN (SELECT task_id FROM runs WHERE state NOT IN ('merged','closed','dropped'))
ORDER BY priority DESC, id`)
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
	deps, err := s.db.QueryContext(ctx, `
SELECT d.task_id, d.depends_on FROM task_deps d
LEFT JOIN tasks t ON t.id = d.depends_on
WHERE t.status NOT IN ('done', 'dropped') OR t.id IS NULL
ORDER BY d.task_id, d.depends_on`)
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
	incidents, err := s.ListOpenIncidents(ctx)
	if err != nil || len(incidents) == 0 {
		return nil, err
	}
	current := incidents[0]
	for _, in := range incidents[1:] {
		if in.CreatedAt > current.CreatedAt || (in.CreatedAt == current.CreatedAt && in.ID > current.ID) {
			current = in
		}
	}
	return current, nil
}

// inQuery expands the placeholder in template into one slot per id.
func inQuery(template string, ids []string) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return fmt.Sprintf(template, strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")), args
}

var _ core.RunStore = (*Store)(nil)
var _ app.ActiveTaskFinder = (*Store)(nil)
var _ app.DecisionSink = (*Store)(nil)
var _ app.DecisionLookup = (*Store)(nil)
var _ app.SnapshotStore = (*Store)(nil)
var _ app.QueueStore = (*Store)(nil)
var _ app.IncidentStore = (*Store)(nil)

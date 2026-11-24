// Package sqlite is the single RunStore implementation.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const runColumns = `id, task_id, state, pr_number, head_sha, observed_sha, observed_base_sha, session_id,
 parent_session_id, workspace_path, branch, scope_json, blocked_incident_id, is_fixer,
 rebase_attempts, fix_attempts, max_attempts, reason, checks_json, created_at, updated_at`

func (s *Store) Save(ctx context.Context, r *core.Run) error {
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
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

func (s *Store) Get(ctx context.Context, id core.RunID) (*core.Run, error) {
	return s.queryOne(ctx, `SELECT `+runColumns+` FROM runs WHERE id=?`, string(id))
}

func (s *Store) FindByPR(ctx context.Context, pr int) (*core.Run, error) {
	return s.queryOne(ctx, `SELECT `+runColumns+` FROM runs WHERE pr_number=? AND state NOT IN ('merged','closed') ORDER BY updated_at DESC LIMIT 1`, pr)
}

func (s *Store) FindActiveByTask(ctx context.Context, task core.TaskID) (*core.Run, error) {
	return s.queryOne(ctx, `SELECT `+runColumns+` FROM runs WHERE task_id=? AND state NOT IN ('merged','closed','dropped') ORDER BY updated_at DESC LIMIT 1`, string(task))
}

func (s *Store) ListActive(ctx context.Context) ([]*core.Run, error) {
	return s.queryAll(ctx, `SELECT `+runColumns+` FROM runs WHERE state NOT IN ('merged','closed') ORDER BY updated_at`)
}

func (s *Store) ListRecent(ctx context.Context, limit int) ([]*core.Run, error) {
	return s.queryAll(ctx, `SELECT `+runColumns+` FROM runs WHERE state IN ('merged','closed','dropped') ORDER BY updated_at DESC LIMIT ?`, limit)
}

func (s *Store) ListByIncident(ctx context.Context, incidentID string) ([]*core.Run, error) {
	return s.queryAll(ctx, `SELECT `+runColumns+` FROM runs WHERE blocked_incident_id=? AND state='blocked'`, incidentID)
}

func (s *Store) queryAll(ctx context.Context, query string, args ...any) ([]*core.Run, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) queryOne(ctx context.Context, query string, args ...any) (*core.Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, query, args...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

type scanner interface{ Scan(...any) error }

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

func (s *Store) ClaimDelivery(ctx context.Context, key string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO deliveries(key, created_at) VALUES (?, ?)`, key, nowUnix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) ReleaseDelivery(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM deliveries WHERE key=?`, key)
	return err
}

func (s *Store) SetApproval(ctx context.Context, id core.RunID, sha string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO approvals(run_id, sha, created_at) VALUES (?,?,?)`, string(id), sha, nowUnix())
	return err
}

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
	const orphan = `SELECT ` + decisionColumns + `, ROW_NUMBER() OVER (ORDER BY at DESC, id DESC) AS rn
FROM decisions WHERE run_id = ''`
	query := `WITH orphan AS (` + orphan + `)
SELECT ` + decisionColumns + ` FROM orphan WHERE rn <= ? ORDER BY at, id`
	args := []any{orphanLimit}
	if len(runIDs) > 0 {
		perRun, ids := inQuery(`per_run AS (SELECT `+decisionColumns+`,
ROW_NUMBER() OVER (PARTITION BY run_id ORDER BY at DESC, id DESC) AS rn
FROM decisions WHERE run_id IN (%s))`, runIDs)
		query = `WITH ` + perRun + `, orphan AS (` + orphan + `)
SELECT ` + decisionColumns + ` FROM per_run WHERE rn <= ?
UNION ALL SELECT ` + decisionColumns + ` FROM orphan WHERE rn <= ?
ORDER BY at, id`
		args = append(ids, limit, orphanLimit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d app.Decision
		var at int64
		var fields string
		if err := rows.Scan(&d.ID, &at, &d.CycleID, &d.Kind, &d.RunID, &d.TaskID,
			&d.PRNumber, &d.Verdict, &d.Action, &d.Reason, &fields); err != nil {
			return nil, err
		}
		d.At = time.Unix(0, at).UTC()
		if err := json.Unmarshal([]byte(fields), &d.Fields); err != nil {
			return nil, fmt.Errorf("fields_json: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DecisionCounts aggregates every decision into a handful of rows. It keeps
// snapshot metrics exact even though ListDecisions returns a bounded sample.
func (s *Store) DecisionCounts(ctx context.Context, runIDs []string) ([]app.DecisionCount, error) {
	out := []app.DecisionCount{}
	query := `SELECT kind, verdict, action, COUNT(*) FROM decisions WHERE run_id = ''
GROUP BY kind, verdict, action`
	var args []any
	if len(runIDs) > 0 {
		query, args = inQuery(`SELECT kind, verdict, action, COUNT(*) FROM decisions
WHERE run_id IN (%s) OR run_id = '' GROUP BY kind, verdict, action`, runIDs)
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
	var d app.Decision
	var at int64
	var fields string
	err := row.Scan(&d.ID, &at, &d.CycleID, &d.Kind, &d.RunID, &d.TaskID,
		&d.PRNumber, &d.Verdict, &d.Action, &d.Reason, &fields)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.At = time.Unix(0, at).UTC()
	if err := json.Unmarshal([]byte(fields), &d.Fields); err != nil {
		return nil, fmt.Errorf("fields_json: %w", err)
	}
	return &d, nil
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

func inQuery(template string, ids []string) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return fmt.Sprintf(template, strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")), args
}

var _ core.RunStore = (*Store)(nil)
var _ app.DecisionSink = (*Store)(nil)
var _ app.DecisionLookup = (*Store)(nil)
var _ app.SnapshotStore = (*Store)(nil)
var _ app.QueueStore = (*Store)(nil)
var _ app.IncidentStore = (*Store)(nil)

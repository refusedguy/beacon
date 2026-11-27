// Package sqlite is the single RunStore implementation.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// State and status literals used by the filtered reads. Each value is defined
// once here; the SQL builders take the values from these definitions, never
// from a caller.
const (
	stateMerged  = string(core.RunMerged)
	stateClosed  = string(core.RunClosed)
	stateDropped = "dropped" // legacy run state, no core constant

	statusAvailable = string(core.TaskAvailable)
	statusTodo      = "todo" // cards use the YAML literal, no core constant
	statusDone      = string(core.TaskDone)
	statusDropped   = string(core.TaskDropped)
)

var (
	// terminalStates is the full finished-run set, including the legacy
	// dropped state still present in older databases.
	terminalStates = []string{stateMerged, stateClosed, stateDropped}
	// activeExcludedStates omits dropped, matching core.RunState.Terminal and
	// the runs_active_updated_idx partial index predicate.
	activeExcludedStates = []string{stateMerged, stateClosed}
	// openTaskStatuses are the statuses of cards a worker may claim.
	openTaskStatuses = []string{statusAvailable, statusTodo}
	// resolvedTaskStatuses are the statuses that unblock a dependency.
	resolvedTaskStatuses = []string{statusDone, statusDropped}
)

// quotedList renders values as a SQL literal list. The active-read predicate
// uses it because a partial index only matches a literal predicate; bound
// parameters there would drop the runs_active_updated_idx scan.
func quotedList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	return strings.Join(quoted, ",")
}

// activeStatesSQL is the literal form of activeExcludedStates for the partial
// index predicate. Both come from the same definitions above.
var activeStatesSQL = quotedList(activeExcludedStates)

// Store is the SQLite implementation of the run and decision stores.
type Store struct{ db *sql.DB }

// Open opens the SQLite state database at path.
func Open(path string) (*Store, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	if err := dropRedundantRunIndexes(db); err != nil {
		_ = db.Close()
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

// Close closes the database handle.
func (s *Store) Close() error { return s.db.Close() }

// runColumns lists the run fields stored in one row.
const runColumns = `id, task_id, state, pr_number, head_sha, observed_sha, observed_base_sha, session_id,
 parent_session_id, workspace_path, branch, scope_json, blocked_incident_id, is_fixer,
 rebase_attempts, fix_attempts, max_attempts, reason, checks_json, created_at, updated_at`

// saveRow holds the encoded JSON columns for one Save write. Timestamps are
// defaulted on the run itself, so callers observe the values that were stored.
type saveRow struct {
	scope  []byte
	checks []byte
}

// prepareSaveRow defaults r's timestamps and encodes its JSON columns.
func prepareSaveRow(r *core.Run, now time.Time) (saveRow, error) {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now
	}
	scope, err := json.Marshal(r.Scope)
	if err != nil {
		return saveRow{}, err
	}
	checks, err := json.Marshal(checksForSave(r.Checks))
	if err != nil {
		return saveRow{}, err
	}
	return saveRow{scope: scope, checks: checks}, nil
}

// writeSaveRow upserts one prepared run and reports whether the row was new
// and which state it held before the write.
func writeSaveRow(ctx context.Context, tx *sql.Tx, r *core.Run, row saveRow) (bool, string, error) {
	var previous string
	newRun := false
	err := tx.QueryRowContext(ctx, `SELECT state FROM runs WHERE id=?`, string(r.ID)).Scan(&previous)
	switch {
	case err == nil:
	case err == sql.ErrNoRows:
		newRun = true
	default:
		return false, "", err
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
		string(row.scope), r.BlockedIncidentID, boolInt(r.IsFixer), r.RebaseAttempts,
		r.FixAttempts, r.MaxAttempts, r.Reason, string(row.checks), r.CreatedAt.UnixNano(), r.UpdatedAt.UnixNano())
	return newRun, previous, err
}

// recordSaveEvent inserts a timeline event when a run enters a new state.
func recordSaveEvent(ctx context.Context, tx *sql.Tx, r *core.Run, newRun bool, previous string) error {
	if !newRun && previous == string(r.State) {
		return nil
	}
	kind, state, at := stateEventKind(r.State), r.State, r.UpdatedAt
	if newRun {
		at = r.CreatedAt
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO run_events (run_id, kind, state, at, head_sha, pr_number, reason) VALUES (?,?,?,?,?,?,?)`,
		string(r.ID), kind, string(state), at.UnixNano(), r.HeadSHA, r.PRNumber, r.Reason)
	return err
}

// Save prepares a row, writes it and records a state event in one transaction.
func (s *Store) Save(ctx context.Context, r *core.Run) error {
	row, err := prepareSaveRow(r, time.Now().UTC())
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	newRun, previous, err := writeSaveRow(ctx, tx, r, row)
	if err != nil {
		return err
	}
	if err := recordSaveEvent(ctx, tx, r, newRun, previous); err != nil {
		return err
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
	query, states := inQuery(`SELECT `+runColumns+` FROM runs WHERE pr_number=? AND state NOT IN (%s) ORDER BY updated_at DESC LIMIT 1`, activeExcludedStates)
	return s.queryOne(ctx, query, append([]any{pr}, states...)...)
}

// FindActiveByTask returns the newest nonterminal run for task.
func (s *Store) FindActiveByTask(ctx context.Context, task core.TaskID) (*core.Run, error) {
	query, states := inQuery(`SELECT `+runColumns+` FROM runs WHERE task_id=? AND state NOT IN (%s) ORDER BY updated_at DESC LIMIT 1`, terminalStates)
	return s.queryOne(ctx, query, append([]any{string(task)}, states...)...)
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
AND state NOT IN (%s)`, taskIDs, terminalStates)
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

// deliveryRetention is the default window for every pruned table. Delivery
// keys and approvals are idempotency markers for a SHA, while decisions and run
// events are audit history. The status snapshot shows only recent history, so
// a row older than this window can no longer change a snapshot or a decision.
const deliveryRetention = 30 * 24 * time.Hour

// retentionBatchSize caps how many rows one prune pass removes from a table.
// The reconcile tick stays short even after a long outage with accumulated
// rows; a later tick removes the next batch.
const retentionBatchSize = 1000

// PruneStale deletes delivery, approval, decision and run event rows older than
// retention. It returns the number of rows removed. A non-positive retention
// uses the default 30-day window. Decisions are deleted through the summary
// delete trigger so DecisionCounts keeps matching the rows still stored.
func (s *Store) PruneStale(ctx context.Context, retention time.Duration) (int64, error) {
	if retention <= 0 {
		retention = deliveryRetention
	}
	cutoff := nowUnix() - retention.Nanoseconds()
	queries := []string{
		`DELETE FROM deliveries WHERE key IN (SELECT key FROM deliveries WHERE created_at < ? LIMIT ?)`,
		`DELETE FROM approvals WHERE rowid IN (SELECT rowid FROM approvals WHERE created_at < ? LIMIT ?)`,
		`DELETE FROM decisions WHERE id IN (SELECT id FROM decisions WHERE at < ? LIMIT ?)`,
		`DELETE FROM run_events WHERE id IN (SELECT id FROM run_events WHERE at < ? LIMIT ?)`,
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

// ListActive returns every run that is not merged or closed. It is a read:
// retention runs on the reconciler ticker, so status, dispatch and MCP reads
// never issue DELETE statements.
func (s *Store) ListActive(ctx context.Context) ([]*core.Run, error) {
	// Keep the state list literal: the partial index predicate must match it
	// exactly, and bound parameters there would force a full scan and sort.
	return s.queryAll(ctx, `SELECT `+runColumns+` FROM runs WHERE state NOT IN (`+activeStatesSQL+`) ORDER BY updated_at`)
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
` + terminal(stateMerged) + `
UNION ALL
` + terminal(stateClosed) + `
UNION ALL
` + terminal(stateDropped) + `
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
	return collectRows(rows, scanRun)
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

// collectRows scans every row and returns the decoded values. The loop, the
// close and the iteration error live here. Each caller supplies its own column
// list through the scan function.
func collectRows[T any](rows *sql.Rows, scan func(scanner) (T, error)) ([]T, error) {
	return collectRowsCap(rows, 0, scan)
}

// collectRowsCap is collectRows with the result slice allocated up front. A
// reader that knows its row count allocates once instead of growing the slice
// through a series of reallocations and copies.
func collectRowsCap[T any](rows *sql.Rows, capacity int, scan func(scanner) (T, error)) ([]T, error) {
	defer rows.Close()
	out := make([]T, 0, capacity)
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// decodeJSON decodes one JSON text column into dst. Every scanner calls this
// so the byte conversion lives in one place. Callers keep the error prefix
// that names the column.
func decodeJSON(text string, dst any) error {
	return json.Unmarshal([]byte(text), dst)
}

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
	if err := decodeJSON(scopeJSON, &r.Scope); err != nil {
		return nil, fmt.Errorf("scope_json: %w", err)
	}
	if err := decodeJSON(checksJSON, &r.Checks); err != nil {
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
	if len(runIDs) == 0 {
		return []app.RunEvent{}, nil
	}
	where, args := inQuery(`run_id IN (%s)`, runIDs)
	// The count pass reads the same index entries as the scan and adds no sort.
	// It gives collectRowsCap the exact size so the event slice is allocated
	// once instead of growing roughly thirteen times for a full snapshot.
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE `+where, args...).Scan(&n); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, run_id, kind, state, at, head_sha, pr_number, reason
FROM run_events WHERE `+where+` ORDER BY at, id`, args...)
	if err != nil {
		return nil, err
	}
	return collectRowsCap(rows, n, scanRunEvent)
}

// scanRunEvent decodes one run event row into an app.RunEvent.
func scanRunEvent(row scanner) (app.RunEvent, error) {
	var event app.RunEvent
	var at int64
	if err := row.Scan(&event.ID, &event.RunID, &event.Kind, &event.State,
		&at, &event.HeadSHA, &event.PRNumber, &event.Reason); err != nil {
		return event, err
	}
	event.At = time.Unix(0, at).UTC()
	return event, nil
}

// decisionColumns lists the decision fields stored in one row.
const decisionColumns = `id, at, cycle_id, kind, run_id, task_id, pr_number, verdict, action, reason, fields_json`

// ListDecisions returns a bounded decision history in one query. Each run keeps
// only its newest decisions; run-less decisions are capped separately so a long
// dispatch history cannot inflate a snapshot. Totals come from DecisionCounts.
func (s *Store) ListDecisions(ctx context.Context, runIDs []string, limit, orphanLimit int) ([]app.Decision, error) {
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
	countQuery, countArgs := decisionCountQuery(runIDs, limit, orphanLimit)
	var n int
	if err := s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&n); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return collectRowsCap(rows, n, func(row scanner) (app.Decision, error) {
		d, err := scanDecision(row)
		if err != nil {
			return app.Decision{}, err
		}
		return *d, nil
	})
}

// decisionCountQuery returns a cheap COUNT statement for the rows ListDecisions
// returns. Each run contributes at most limit rows and the orphan bucket at
// most orphanLimit, so MIN over each group gives the exact size without running
// the window function a second time.
func decisionCountQuery(runIDs []string, limit, orphanLimit int) (string, []any) {
	const orphan = `SELECT MIN(COUNT(*), ?) AS n FROM decisions WHERE run_id = ''`
	if len(runIDs) == 0 {
		return `SELECT COALESCE(SUM(n), 0) FROM (` + orphan + `)`, []any{orphanLimit}
	}
	perRun, ids := inQuery(`SELECT MIN(COUNT(*), ?) AS n FROM decisions WHERE run_id IN (%s) GROUP BY run_id`, runIDs)
	query := `SELECT COALESCE(SUM(n), 0) FROM (
` + perRun + `
UNION ALL
` + orphan + `
)`
	return query, append(append([]any{limit}, ids...), orphanLimit)
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
	if err := decodeJSON(fields, &d.Fields); err != nil {
		return nil, fmt.Errorf("fields_json: %w", err)
	}
	return &d, nil
}

// DecisionCounts aggregates every decision into a handful of rows. It keeps
// snapshot metrics exact even though ListDecisions returns a bounded sample.
func (s *Store) DecisionCounts(ctx context.Context, runIDs []string) ([]app.DecisionCount, error) {
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
	return collectRows(rows, scanDecisionCount)
}

// scanDecisionCount decodes one aggregate row into an app.DecisionCount.
func scanDecisionCount(row scanner) (app.DecisionCount, error) {
	var c app.DecisionCount
	if err := row.Scan(&c.Kind, &c.Verdict, &c.Action, &c.Count); err != nil {
		return c, err
	}
	return c, nil
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
	// A missing dependency row is gone, not pending. Match readyTasksQuery,
	// which joins task_deps to tasks, so a deleted card stops blocking the
	// monitor's queue instead of showing a phantom waiting_deps entry.
	depsQuery, depsArgs := inQuery(`SELECT d.task_id, d.depends_on FROM task_deps d
JOIN tasks t ON t.id = d.depends_on
WHERE t.status NOT IN (%s)
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

// inQuery expands one %s per list in template into bound placeholders. It
// returns the values for those placeholders in list order. Both the argument
// slice and each placeholder string are sized once, so a snapshot with a large
// run-id set does not grow them a slice at a time.
func inQuery(template string, lists ...[]string) (string, []any) {
	holders := make([]any, len(lists))
	total := 0
	for _, list := range lists {
		total += len(list)
	}
	args := make([]any, 0, total)
	for i, list := range lists {
		var b strings.Builder
		if len(list) > 0 {
			b.Grow(2*len(list) - 1)
		}
		for j := range list {
			if j > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('?')
		}
		holders[i] = b.String()
		for _, v := range list {
			args = append(args, v)
		}
	}
	return fmt.Sprintf(template, holders...), args
}

var _ core.RunStore = (*Store)(nil)
var _ app.ActiveTaskFinder = (*Store)(nil)
var _ app.DecisionSink = (*Store)(nil)
var _ app.DecisionLookup = (*Store)(nil)
var _ app.SnapshotStore = (*Store)(nil)
var _ app.QueueStore = (*Store)(nil)
var _ app.IncidentStore = (*Store)(nil)

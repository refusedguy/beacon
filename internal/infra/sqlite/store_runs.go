package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

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
// An active run may not take a PR another active run already owns. Dispatch
// binds a PR at spawn and WatchPR binds one by operator action, and both call
// Save, so the guard keeps one active owner per PR at both entry points.
func (s *Store) Save(ctx context.Context, r *core.Run) error {
	if err := s.checkActivePR(ctx, r); err != nil {
		return err
	}
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

// checkActivePR refuses a second active run on a PR. A terminal run keeps its
// PR number as history, so only nonterminal rows take part in the lookup.
func (s *Store) checkActivePR(ctx context.Context, r *core.Run) error {
	if r.State.Terminal() || r.PRNumber <= 0 {
		return nil
	}
	query, states := inQuery(`SELECT id FROM runs WHERE pr_number=? AND id<>? AND state NOT IN (%s)
ORDER BY updated_at DESC LIMIT 1`, activeExcludedStates)
	var owner string
	err := s.db.QueryRowContext(ctx, query, append([]any{r.PRNumber, string(r.ID)}, states...)...).Scan(&owner)
	switch {
	case err == nil:
		return core.Errorf(core.CodeConflict, "PR %d already belongs to active run %s", r.PRNumber, owner)
	case err == sql.ErrNoRows:
		return nil
	default:
		return err
	}
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

// ActiveRunsByPR returns every active run bound to pr, oldest first. A
// reconciler uses it to detect a duplicate binding that the newest-row lookup
// in FindByPR would otherwise hide.
func (s *Store) ActiveRunsByPR(ctx context.Context, pr int) ([]*core.Run, error) {
	query, states := inQuery(`SELECT `+runColumns+` FROM runs WHERE pr_number=? AND state NOT IN (%s)
ORDER BY updated_at`, activeExcludedStates)
	return s.queryAll(ctx, query, append([]any{pr}, states...)...)
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
	predicate := `state NOT IN (` + activeStatesSQL + `)`
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE `+predicate).Scan(&n); err != nil {
		return nil, err
	}
	return s.queryAllCap(ctx, n, `SELECT `+runColumns+` FROM runs WHERE `+predicate+` ORDER BY updated_at`)
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
	return s.queryAllCap(ctx, 0, query, args...)
}

// queryAllCap is queryAll with the result slice allocated up front. A reader
// that already knows its row count allocates once instead of growing the slice
// through repeated reallocations and copies.
func (s *Store) queryAllCap(ctx context.Context, capacity int, query string, args ...any) ([]*core.Run, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return collectRowsCap(rows, capacity, scanRun)
}

// queryOne runs query and scans at most one row into a run.
func (s *Store) queryOne(ctx context.Context, query string, args ...any) (*core.Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, query, args...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
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

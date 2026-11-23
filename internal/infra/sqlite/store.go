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
	store := &Store{db: db}
	app.SetDecisionSink(store)
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

const runColumns = `id, task_id, state, pr_number, head_sha, observed_sha, observed_base_sha, session_id,
 parent_session_id, workspace_path, branch, scope_json, blocked_incident_id, is_fixer,
 rebase_attempts, fix_attempts, max_attempts, reason, created_at, updated_at`

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
INSERT INTO runs (`+runColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
 state=excluded.state, pr_number=excluded.pr_number, head_sha=excluded.head_sha,
 observed_sha=excluded.observed_sha, observed_base_sha=excluded.observed_base_sha,
 session_id=excluded.session_id,
 parent_session_id=excluded.parent_session_id, workspace_path=excluded.workspace_path,
 branch=excluded.branch, scope_json=excluded.scope_json,
 blocked_incident_id=excluded.blocked_incident_id, is_fixer=excluded.is_fixer,
 rebase_attempts=excluded.rebase_attempts, fix_attempts=excluded.fix_attempts,
 max_attempts=excluded.max_attempts, reason=excluded.reason, updated_at=excluded.updated_at`,
		string(r.ID), string(r.TaskID), string(r.State), r.PRNumber, r.HeadSHA,
		r.ObservedSHA, r.ObservedBaseSHA, r.SessionID, r.ParentSessionID, r.WorkspacePath, r.Branch,
		string(scope), r.BlockedIncidentID, boolInt(r.IsFixer), r.RebaseAttempts,
		r.FixAttempts, r.MaxAttempts, r.Reason, r.CreatedAt.UnixNano(), r.UpdatedAt.UnixNano())
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
	var id, task, state, scopeJSON string
	var created, updated int64
	var fixer int
	err := row.Scan(&id, &task, &state, &r.PRNumber, &r.HeadSHA, &r.ObservedSHA,
		&r.ObservedBaseSHA, &r.SessionID, &r.ParentSessionID, &r.WorkspacePath, &r.Branch, &scopeJSON,
		&r.BlockedIncidentID, &fixer, &r.RebaseAttempts, &r.FixAttempts,
		&r.MaxAttempts, &r.Reason, &created, &updated)
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

// ListDecisions returns the decision history of all given runs in one query.
// Decisions without a run (dispatch skips, candidate counts) are included so
// their counts reach the snapshot metrics.
func (s *Store) ListDecisions(ctx context.Context, runIDs []string) ([]app.Decision, error) {
	out := []app.Decision{}
	query := `SELECT ` + decisionColumns + ` FROM decisions WHERE run_id = '' ORDER BY at, id`
	var args []any
	if len(runIDs) > 0 {
		query, args = inQuery(`SELECT `+decisionColumns+` FROM decisions
WHERE run_id IN (%s) OR run_id = '' ORDER BY at, id`, runIDs)
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

func inQuery(template string, ids []string) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return fmt.Sprintf(template, strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")), args
}

var _ core.RunStore = (*Store)(nil)
var _ app.DecisionSink = (*Store)(nil)
var _ app.SnapshotStore = (*Store)(nil)

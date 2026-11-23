// Package sqlite is the single RunStore implementation.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

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

const runColumns = `id, task_id, state, pr_number, head_sha, observed_sha, session_id,
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
	_, err = s.db.ExecContext(ctx, `
INSERT INTO runs (`+runColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
 state=excluded.state, pr_number=excluded.pr_number, head_sha=excluded.head_sha,
 observed_sha=excluded.observed_sha, session_id=excluded.session_id,
 parent_session_id=excluded.parent_session_id, workspace_path=excluded.workspace_path,
 branch=excluded.branch, scope_json=excluded.scope_json,
 blocked_incident_id=excluded.blocked_incident_id, is_fixer=excluded.is_fixer,
 rebase_attempts=excluded.rebase_attempts, fix_attempts=excluded.fix_attempts,
 max_attempts=excluded.max_attempts, reason=excluded.reason, updated_at=excluded.updated_at`,
		string(r.ID), string(r.TaskID), string(r.State), r.PRNumber, r.HeadSHA,
		r.ObservedSHA, r.SessionID, r.ParentSessionID, r.WorkspacePath, r.Branch,
		string(scope), r.BlockedIncidentID, boolInt(r.IsFixer), r.RebaseAttempts,
		r.FixAttempts, r.MaxAttempts, r.Reason, r.CreatedAt.UnixNano(), r.UpdatedAt.UnixNano())
	return err
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
		&r.SessionID, &r.ParentSessionID, &r.WorkspacePath, &r.Branch, &scopeJSON,
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

var _ core.RunStore = (*Store)(nil)

package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// newIncidentID generates a random base incident identifier.
func newIncidentID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("inc-%d", time.Now().UnixNano())
	}
	return "inc-" + hex.EncodeToString(b[:])
}

// CreateIncident stores in or returns the open incident with the same signature.
func (s *Store) CreateIncident(ctx context.Context, in *core.BaseIncident) (*core.BaseIncident, error) {
	if existing, err := s.FindOpenIncident(ctx, in.Repo, in.BaseBranch, in.Signature); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}
	if in.ID == "" {
		in.ID = newIncidentID()
	}
	if in.CreatedAt == 0 {
		in.CreatedAt = nowUnix()
	}
	if in.Status == "" {
		in.Status = "open"
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO base_incidents(id, repo, base_branch, signature, failed_checks_json,
 fixer_run_id, status, attempts, created_at, resolved_at)
VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(repo, base_branch, signature) DO UPDATE SET
 id=excluded.id, failed_checks_json=excluded.failed_checks_json,
 fixer_run_id='', status='open', attempts=0,
 created_at=excluded.created_at, resolved_at=0
WHERE base_incidents.status != 'open'`,
		in.ID, in.Repo, in.BaseBranch, in.Signature, in.FailedChecksJSON,
		string(in.FixerRunID), in.Status, in.Attempts, in.CreatedAt, in.ResolvedAt)
	if err != nil {
		return nil, err
	}
	return s.FindOpenIncident(ctx, in.Repo, in.BaseBranch, in.Signature)
}

// GetIncident returns the incident with id, or nil when it does not exist.
func (s *Store) GetIncident(ctx context.Context, id string) (*core.BaseIncident, error) {
	return s.scanIncident(s.db.QueryRowContext(ctx, `SELECT `+incidentColumns+` FROM base_incidents WHERE id=?`, id))
}

// FindOpenIncident returns the open incident matching repo, base and signature.
func (s *Store) FindOpenIncident(ctx context.Context, repo, base, signature string) (*core.BaseIncident, error) {
	return s.scanIncident(s.db.QueryRowContext(ctx,
		`SELECT `+incidentColumns+` FROM base_incidents WHERE repo=? AND base_branch=? AND signature=? AND status='open'`,
		repo, base, signature))
}

// ListOpenIncidents returns every incident that is still open.
func (s *Store) ListOpenIncidents(ctx context.Context) ([]*core.BaseIncident, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+incidentColumns+` FROM base_incidents WHERE status='open'`)
	if err != nil {
		return nil, err
	}
	return collectRows(rows, s.scanIncident)
}

// incidentColumns lists the base incident fields stored in one row.
const incidentColumns = `id, repo, base_branch, signature, failed_checks_json,
 fixer_run_id, status, attempts, created_at, resolved_at`

// scanIncident decodes one database row into a base incident.
func (s *Store) scanIncident(row scanner) (*core.BaseIncident, error) {
	var in core.BaseIncident
	var fixer string
	err := row.Scan(&in.ID, &in.Repo, &in.BaseBranch, &in.Signature, &in.FailedChecksJSON,
		&fixer, &in.Status, &in.Attempts, &in.CreatedAt, &in.ResolvedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	in.FixerRunID = core.RunID(fixer)
	return &in, nil
}

// UpdateIncident writes the mutable fields of in back to the database.
func (s *Store) UpdateIncident(ctx context.Context, in *core.BaseIncident) error {
	if in.Status == "resolved" && in.ResolvedAt == 0 {
		in.ResolvedAt = nowUnix()
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE base_incidents SET failed_checks_json=?, fixer_run_id=?, status=?, attempts=?, resolved_at=? WHERE id=?`,
		in.FailedChecksJSON, string(in.FixerRunID), in.Status, in.Attempts, in.ResolvedAt, in.ID)
	return err
}

// FailedChecksJSON encodes check results for the failed_checks_json column.
// A check whose start or completion time falls outside years 0 to 9999 makes
// time.Time.MarshalJSON fail. Log that error and store an empty array so the
// column still holds valid JSON.
func FailedChecksJSON(checks []core.CheckResult) string {
	b, err := json.Marshal(checks)
	if err != nil {
		slog.Error("encode incident failed checks", "error", err)
		return "[]"
	}
	return string(b)
}

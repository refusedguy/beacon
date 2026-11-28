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

// SaveOperation stores the outcome of one idempotent mutation. An empty
// errText records success; a non-empty one records the failure so a retry
// receives the original error instead of running the mutation again. The first
// record wins: a late racing writer cannot replace a stored outcome.
func (s *Store) SaveOperation(ctx context.Context, id, payload, errText string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO operation_results (operation_id, payload, error, created_at)
VALUES (?,?,?,?)`, id, payload, errText, nowUnix())
	return err
}

// LoadOperation returns the stored payload and error text for id. found is
// false when no operation with that id has completed.
func (s *Store) LoadOperation(ctx context.Context, id string) (payload, errText string, found bool, err error) {
	var storedPayload, storedError string
	err = s.db.QueryRowContext(ctx,
		`SELECT payload, error FROM operation_results WHERE operation_id=?`, id).
		Scan(&storedPayload, &storedError)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return storedPayload, storedError, true, nil
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

// RunExplain implements app.RunExplainStore. It reads one run by primary key,
// its timeline through run_events_run_at_idx and its newest decision through
// decisions_run_at_idx. It never lists runs or scans another run's history.
func (s *Store) RunExplain(ctx context.Context, id core.RunID) (*app.RunExplainData, error) {
	run, err := s.Get(ctx, id)
	if err != nil || run == nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, run_id, kind, state, at, head_sha, pr_number, reason
FROM run_events WHERE run_id=? ORDER BY at, id`, string(id))
	if err != nil {
		return nil, err
	}
	events, err := collectRows(rows, scanRunEvent)
	if err != nil {
		return nil, err
	}
	decision, err := s.LastDecision(ctx, string(id))
	if err != nil {
		return nil, err
	}
	return &app.RunExplainData{Run: run, Events: events, Decision: decision}, nil
}

// DecisionsSince returns every decision recorded at or after since, oldest
// first. Unlike ListDecisions it has no per-run cap: the window is bounded by
// retention and a report needs the whole set for its counts.
func (s *Store) DecisionsSince(ctx context.Context, since time.Time) ([]app.Decision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+decisionColumns+` FROM decisions
WHERE at>=? ORDER BY at, id`, since.UnixNano())
	if err != nil {
		return nil, err
	}
	return collectRows(rows, func(row scanner) (app.Decision, error) {
		d, err := scanDecision(row)
		if err != nil {
			return app.Decision{}, err
		}
		return *d, nil
	})
}

// EventsSince returns every run event recorded at or after since, oldest
// first. The at index serves the range scan.
func (s *Store) EventsSince(ctx context.Context, since time.Time) ([]app.RunEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, run_id, kind, state, at, head_sha, pr_number, reason
FROM run_events WHERE at>=? ORDER BY at, id`, since.UnixNano())
	if err != nil {
		return nil, err
	}
	return collectRows(rows, scanRunEvent)
}

// IncidentsSince returns incidents opened in the window, resolved in the
// window, or still open. The open set includes incidents created before since,
// so a shift report can name an incident that crossed the boundary.
func (s *Store) IncidentsSince(ctx context.Context, since time.Time) ([]*core.BaseIncident, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+incidentColumns+` FROM base_incidents
WHERE created_at>=? OR resolved_at>=? OR status='open' ORDER BY created_at, id`,
		since.UnixNano(), since.UnixNano())
	if err != nil {
		return nil, err
	}
	return collectRows(rows, s.scanIncident)
}

// RunsSince returns runs that reached a terminal state at or after since and
// every run still active. The active rows let a report name a run whose
// activity stopped before the window began.
func (s *Store) RunsSince(ctx context.Context, since time.Time) ([]*core.Run, error) {
	return s.queryAll(ctx, `SELECT `+runColumns+` FROM runs
WHERE updated_at>=? OR state NOT IN (`+activeStatesSQL+`) ORDER BY updated_at`, since.UnixNano())
}

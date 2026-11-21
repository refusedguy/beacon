// Package store: durable ACP runs (T3.278).
//
// One ACP run belongs to one task line. The task id is the primary key, so a
// second spawn for the same line returns the first record instead of starting
// a second piece of work. The record survives the command that created it and
// lives in the same beacon.db as sessions and runtime stats (ADR 0086).
//
// Writes create the table on first use. Reads never create it, so a store that
// saw no ACP run yet answers with an empty result.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// ACPRunState is one state of a durable ACP run. The four values are the
// states named by T3.278. They are stored as text so the table needs no
// schema change when a caller adds a state.
type ACPRunState string

const (
	ACPRunRequested   ACPRunState = "requested"
	ACPRunStarted     ACPRunState = "started"
	ACPRunFinished    ACPRunState = "finished"
	ACPRunInterrupted ACPRunState = "interrupted"
)

// ACPRun is one persisted run. TaskID is the task line and the stable key.
// SessionID is the opaque ACP session id, empty until the runner creates one.
// Result holds the last stream payload, written when the run finishes or is
// interrupted so a later invocation can read it after a restart.
type ACPRun struct {
	TaskID    string
	SessionID string
	Runner    string
	Title     string
	State     ACPRunState
	Result    json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

// createACPRunsSQL is issued on first write. task_id keeps one run per task
// line; result is nullable because a claimed run has no output yet.
const createACPRunsSQL = `
CREATE TABLE IF NOT EXISTS acp_runs (
  task_id    TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  runner     TEXT NOT NULL,
  title      TEXT NOT NULL,
  state      TEXT NOT NULL,
  result     BLOB,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
`

// CanTransitionACPRun reports whether to is a legal next state from from.
// A finished run is final: a repeated spawn returns its stored result instead
// of running the task again. An interrupted run may start again on the same
// session when a new invocation attaches to it.
func CanTransitionACPRun(from, to ACPRunState) bool {
	switch from {
	case ACPRunRequested:
		return to == ACPRunStarted || to == ACPRunInterrupted
	case ACPRunStarted:
		return to == ACPRunFinished || to == ACPRunInterrupted
	case ACPRunInterrupted:
		return to == ACPRunStarted
	default:
		return false
	}
}

// ClaimACPRun inserts run when the task has no record yet and returns the row
// that is stored afterwards. created is true only when this call inserted the
// row. The insert and the read share one write transaction, so two callers
// asking for the same task cannot both create a run.
func (d *DB) ClaimACPRun(run ACPRun) (ACPRun, bool, error) {
	if d == nil {
		return ACPRun{}, false, errors.New("store: acp runs: nil db")
	}
	taskID := strings.TrimSpace(run.TaskID)
	if taskID == "" {
		return ACPRun{}, false, domain.NewAppError(domain.CodeInvalidInput, "store: acp runs: empty task id")
	}
	if run.State == "" {
		run.State = ACPRunRequested
	}
	now := time.Now().UTC()
	createdAt := run.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	updatedAt := run.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = now
	}
	var stored ACPRun
	var created bool
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createACPRunsSQL); err != nil {
			return fmt.Errorf("store: acp runs: create table: %w", err)
		}
		res, err := tx.Exec(`
			INSERT INTO acp_runs (task_id, session_id, runner, title, state, result, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(task_id) DO NOTHING`,
			taskID, run.SessionID, run.Runner, run.Title, string(run.State), nullJSON(run.Result),
			timeToNano(createdAt), timeToNano(updatedAt))
		if err != nil {
			return fmt.Errorf("store: acp runs: claim: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: acp runs: claim rows: %w", err)
		}
		created = n == 1
		stored, err = scanACPRun(tx.QueryRowContext(context.Background(), `
			SELECT task_id, session_id, runner, title, state, result, created_at, updated_at
			FROM acp_runs WHERE task_id = ?`, taskID))
		if err != nil {
			return fmt.Errorf("store: acp runs: read after claim: %w", err)
		}
		return nil
	})
	return stored, created, err
}

// GetACPRun reads the run for task. found is false when no run was ever
// claimed for that task. The read never creates the table.
func (d *DB) GetACPRun(task string) (ACPRun, bool, error) {
	if d == nil {
		return ACPRun{}, false, errors.New("store: acp runs: nil db")
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return ACPRun{}, false, domain.NewAppError(domain.CodeInvalidInput, "store: acp runs: empty task id")
	}
	ctx := context.Background()
	ok, err := tableExists(ctx, d.readDB, "acp_runs")
	if err != nil || !ok {
		return ACPRun{}, false, err
	}
	run, err := scanACPRun(d.readDB.QueryRowContext(ctx, `
		SELECT task_id, session_id, runner, title, state, result, created_at, updated_at
		FROM acp_runs WHERE task_id = ?`, task))
	if errors.Is(err, sql.ErrNoRows) {
		return ACPRun{}, false, nil
	}
	if err != nil {
		return ACPRun{}, false, err
	}
	return run, true, nil
}

// ListACPRuns returns every run ordered by creation time, then by task id.
func (d *DB) ListACPRuns() ([]ACPRun, error) {
	if d == nil {
		return nil, errors.New("store: acp runs: nil db")
	}
	ctx := context.Background()
	ok, err := tableExists(ctx, d.readDB, "acp_runs")
	if err != nil || !ok {
		return nil, err
	}
	rows, err := d.readDB.QueryContext(ctx, `
		SELECT task_id, session_id, runner, title, state, result, created_at, updated_at
		FROM acp_runs ORDER BY created_at, task_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ACPRun{}
	for rows.Next() {
		run, err := scanACPRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// TransitionACPRun moves task to to when the current state allows it. An
// empty sessionID or result keeps the stored value, so a caller can update
// only the state. changed is false when the task has no run or when the state
// machine rejects the move; the stored row is untouched in that case.
func (d *DB) TransitionACPRun(task string, to ACPRunState, sessionID string, result json.RawMessage) (bool, error) {
	if d == nil {
		return false, errors.New("store: acp runs: nil db")
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return false, domain.NewAppError(domain.CodeInvalidInput, "store: acp runs: empty task id")
	}
	if to == "" {
		return false, domain.NewAppError(domain.CodeInvalidInput, "store: acp runs: empty state")
	}
	var changed bool
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createACPRunsSQL); err != nil {
			return fmt.Errorf("store: acp runs: create table: %w", err)
		}
		var state string
		if err := tx.QueryRowContext(context.Background(),
			`SELECT state FROM acp_runs WHERE task_id = ?`, task).Scan(&state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("store: acp runs: read state: %w", err)
		}
		if !CanTransitionACPRun(ACPRunState(state), to) {
			return nil
		}
		_, err := tx.Exec(`
			UPDATE acp_runs SET
			  state = ?,
			  session_id = CASE WHEN ? = '' THEN session_id ELSE ? END,
			  result = CASE WHEN ? IS NULL THEN result ELSE ? END,
			  updated_at = ?
			WHERE task_id = ?`,
			string(to), sessionID, sessionID, nullJSON(result), nullJSON(result),
			timeToNano(time.Now().UTC()), task)
		if err != nil {
			return fmt.Errorf("store: acp runs: transition: %w", err)
		}
		changed = true
		return nil
	})
	return changed, err
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

type acpRunScanner interface {
	Scan(dest ...any) error
}

func scanACPRun(row acpRunScanner) (ACPRun, error) {
	var run ACPRun
	var state string
	var result []byte
	var createdAt, updatedAt int64
	if err := row.Scan(&run.TaskID, &run.SessionID, &run.Runner, &run.Title,
		&state, &result, &createdAt, &updatedAt); err != nil {
		return ACPRun{}, err
	}
	run.State = ACPRunState(state)
	if len(result) != 0 {
		run.Result = append(json.RawMessage(nil), result...)
	}
	run.CreatedAt = timeFromNano(createdAt)
	run.UpdatedAt = timeFromNano(updatedAt)
	return run, nil
}

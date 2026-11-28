// Package sqlite is the single RunStore implementation.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

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
	if err := ensureOperationResults(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureWaitlist(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the database handle.
func (s *Store) Close() error { return s.db.Close() }

// operationResultsDDL is the durable half of the MCP mutation journal. A row
// holds the JSON result payload on success and the error text on failure, so a
// retry after a restart receives the original outcome.
const operationResultsDDL = `CREATE TABLE IF NOT EXISTS operation_results (
  operation_id TEXT PRIMARY KEY,
  payload TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
)`

// ensureOperationResults adds the journal table to every state database,
// including one opened before the table existed. It runs outside the schema
// version marker so an already prepared database gains the table on open.
func ensureOperationResults(db *sql.DB) error {
	_, err := db.Exec(operationResultsDDL)
	return err
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

// Trivial JSON column values. Most rows store one of these, so decoding them
// through json.Unmarshal buys nothing and pays for reflection on every scan.
const (
	jsonEmpty       = ""
	jsonEmptyObject = "{}"
	jsonEmptyArray  = "[]"
	jsonNull        = "null"
)

// decodeJSON decodes one JSON text column into dst. Empty and trivial values
// reset dst directly, which skips json.Unmarshal on the common case. Every
// scanner calls this so the byte conversion lives in one place. Callers keep
// the error prefix that names the column.
func decodeJSON(text string, dst any) error {
	if text == jsonEmpty || text == jsonEmptyObject || text == jsonEmptyArray || text == jsonNull {
		switch d := dst.(type) {
		case *[]core.ScopePath:
			*d = nil
			return nil
		case *[]core.CheckResult:
			*d = nil
			return nil
		case *map[string]any:
			*d = nil
			return nil
		}
	}
	return json.Unmarshal([]byte(text), dst)
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

// WaitEntry is one card waiting on one scope path. WaitingSince is a Unix
// nanosecond reading: a smaller value waited longer.
type WaitEntry struct {
	Path         core.ScopePath
	TaskID       core.TaskID
	WaitingSince int64
}

// AddWaitEntry inserts or refreshes a wait entry for each path a candidate
// lost on. waitingSince comes from the caller's clock so a caller can keep an
// original wait time across repeated conflicts. A repeat call for the same
// path and card refreshes the existing row instead of adding a second one.
func (s *Store) AddWaitEntry(ctx context.Context, taskID core.TaskID, paths []core.ScopePath, waitingSince int64) error {
	if len(paths) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO waitlist (path, task_id, waiting_since) VALUES (?, ?, ?)
ON CONFLICT (path, task_id) DO UPDATE SET waiting_since = excluded.waiting_since`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, p := range paths {
		if _, err := stmt.ExecContext(ctx, string(p), string(taskID), waitingSince); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LongestWaiter returns the entry that has waited longest across paths, or nil
// when none of them has a waiter. The age index bounds the lookup, so task
// bodies are never read. Ties break on task ID then path for a stable answer.
func (s *Store) LongestWaiter(ctx context.Context, paths []core.ScopePath) (*WaitEntry, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	values := make([]string, len(paths))
	for i, p := range paths {
		values[i] = string(p)
	}
	query, args := inQuery(`SELECT path, task_id, waiting_since FROM waitlist
WHERE path IN (%s)
ORDER BY waiting_since ASC, task_id ASC, path ASC LIMIT 1`, values)
	var entry WaitEntry
	var path, taskID string
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&path, &taskID, &entry.WaitingSince); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	entry.Path = core.ScopePath(path)
	entry.TaskID = core.TaskID(taskID)
	return &entry, nil
}

// RemoveWaitEntries clears every wait entry for a card once it runs. Entries
// for other cards stay, so a path still held by a different owner keeps its
// waiter.
func (s *Store) RemoveWaitEntries(ctx context.Context, taskID core.TaskID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM waitlist WHERE task_id = ?`, string(taskID))
	return err
}

var _ core.RunStore = (*Store)(nil)
var _ app.ActiveTaskFinder = (*Store)(nil)
var _ app.DecisionSink = (*Store)(nil)
var _ app.DecisionLookup = (*Store)(nil)
var _ app.SnapshotStore = (*Store)(nil)
var _ app.QueueStore = (*Store)(nil)
var _ app.IncidentStore = (*Store)(nil)
var _ app.ReportStore = (*Store)(nil)

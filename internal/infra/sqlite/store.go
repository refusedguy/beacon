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

// Open opens the SQLite state database at path. openDB prepares the schema in
// one transaction, so there is no DDL left to apply here.
func Open(path string) (*Store, error) {
	db, err := openDB(path)
	if err != nil {
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

// ensureOperationResults adds the journal table during schema preparation. An
// older database that lacks it fails schemaPrepared and takes the migration
// path, so it gains the table on the next open.
func ensureOperationResults(ctx context.Context, db dbExecutor) error {
	_, err := db.ExecContext(ctx, operationResultsDDL)
	return err
}

// SaveCAS saves r only while the stored row still carries the version the
// caller read. expectedUpdatedAt is a Unix nanosecond reading and
// expectedHeadSHA is the head SHA of that same read. Both checks and the write
// share one immediate transaction, so a save that lands between the caller's
// read and this call fails instead of overwriting the newer state.
//
// A stale version returns a core.CodeConflict error. A run that disappeared
// returns core.CodeNotFound. Callers that need to retry can read the run again
// and decide from its current state.
func (s *Store) SaveCAS(ctx context.Context, r *core.Run, expectedUpdatedAt int64, expectedHeadSHA string) error {
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
	var storedUpdatedAt int64
	var storedHeadSHA string
	err = tx.QueryRowContext(ctx, `SELECT updated_at, head_sha FROM runs WHERE id=?`, string(r.ID)).
		Scan(&storedUpdatedAt, &storedHeadSHA)
	switch {
	case err == sql.ErrNoRows:
		return core.Errorf(core.CodeNotFound, "run %s no longer exists", r.ID)
	case err != nil:
		return err
	case storedUpdatedAt != expectedUpdatedAt || storedHeadSHA != expectedHeadSHA:
		return core.Errorf(core.CodeConflict, "run %s changed since it was read", r.ID)
	}
	newRun, previous, err := writeSaveRow(ctx, tx, r, row)
	if err != nil {
		return err
	}
	if err := recordSaveEvent(ctx, tx, r, newRun, previous); err != nil {
		return err
	}
	return tx.Commit()
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

// deliveryRetention is the default window for every age-pruned table. Delivery
// keys and approvals are idempotency markers for a SHA, while decisions and run
// events are audit history. The status snapshot shows only recent history, so a
// row older than this window can no longer change a snapshot or a decision.
const deliveryRetention = 30 * 24 * time.Hour

// retentionBatchSize caps how many rows one prune pass removes from a table.
// The reconcile tick stays short even after a long outage with accumulated
// rows; a later tick removes the next batch.
const retentionBatchSize = 1000

// terminalRunLimit is the number of newest terminal runs a prune keeps. It is
// a variable so the boundary test can use a small limit without writing twenty
// thousand rows.
var terminalRunLimit = 20000

// terminalStatesSQL is the literal state list used by the terminal prune. It
// shares the definitions used by the read-side filters.
var terminalStatesSQL = quotedList(terminalStates)

// PruneStale deletes delivery, approval, decision and run event rows older than
// retention, then trims terminal runs past terminalRunLimit. It returns the
// number of rows removed. A non-positive retention uses the default 30-day
// window. Decisions are deleted through the summary delete trigger so
// DecisionCounts keeps matching the rows still stored.
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
	runs, err := pruneTerminalRuns(ctx, s.db)
	removed += runs
	return removed, err
}

// pruneTerminalRuns deletes the oldest terminal runs beyond terminalRunLimit
// in batches. A run named by base_incidents.fixer_run_id is excluded from the
// ranking, so an incident never points at a deleted run. Freed pages return to
// the SQLite freelist and are reused by later inserts, so the file stops
// growing at its high-water mark. The batch cap keeps one reconciler tick
// short; a later tick removes the next batch.
//
// There is no automatic VACUUM. A measurement with 20,000 run rows on a 5,699 page
// database showed a prune of 1,000 rows costing 87 ms and leaving 282 freelist
// pages. Inserting 500 new rows reused that space without growing the file.
// VACUUM took 146 ms under an exclusive lock and only lowered the high-water
// mark to 5,347 pages. PRAGMA incremental_vacuum was a no-op because these
// databases use auto_vacuum=NONE; enabling it would need a one-time full rewrite
// of every existing state.db. The operator can run a VACUUM during an outage if
// the freed pages must go back to the filesystem.
func pruneTerminalRuns(ctx context.Context, db *sql.DB) (int64, error) {
	query := `DELETE FROM runs WHERE id IN (
 SELECT id FROM (
   SELECT id FROM runs
   WHERE state IN (` + terminalStatesSQL + `)
     AND id NOT IN (SELECT fixer_run_id FROM base_incidents WHERE fixer_run_id <> '')
   ORDER BY updated_at DESC
   LIMIT ` + strconv.Itoa(retentionBatchSize) + ` OFFSET ` + strconv.Itoa(terminalRunLimit) + `
 )
)`
	var removed int64
	for {
		res, err := db.ExecContext(ctx, query)
		if err != nil {
			return removed, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return removed, err
		}
		removed += n
		if n < retentionBatchSize {
			return removed, nil
		}
	}
}

var _ core.RunStore = (*Store)(nil)
var _ app.ActiveTaskFinder = (*Store)(nil)
var _ app.DecisionSink = (*Store)(nil)
var _ app.DecisionLookup = (*Store)(nil)
var _ app.SnapshotStore = (*Store)(nil)
var _ app.QueueStore = (*Store)(nil)
var _ app.IncidentStore = (*Store)(nil)
var _ app.ReportStore = (*Store)(nil)

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const schema = `
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL,
  state TEXT NOT NULL,
  pr_number INTEGER NOT NULL DEFAULT 0,
  head_sha TEXT NOT NULL DEFAULT '',
  observed_sha TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  parent_session_id TEXT NOT NULL DEFAULT '',
  workspace_path TEXT NOT NULL DEFAULT '',
  branch TEXT NOT NULL DEFAULT '',
  scope_json TEXT NOT NULL DEFAULT '[]',
  blocked_incident_id TEXT NOT NULL DEFAULT '',
  is_fixer INTEGER NOT NULL DEFAULT 0,
  rebase_attempts INTEGER NOT NULL DEFAULT 0,
  fix_attempts INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 12,
  reason TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS runs_pr_idx ON runs(pr_number);
CREATE INDEX IF NOT EXISTS runs_task_idx ON runs(task_id);
CREATE INDEX IF NOT EXISTS runs_pr_updated_idx ON runs(pr_number, updated_at DESC);
CREATE INDEX IF NOT EXISTS runs_task_updated_idx ON runs(task_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS runs_active_updated_idx ON runs(updated_at) WHERE state NOT IN ('merged','closed');
CREATE INDEX IF NOT EXISTS runs_incident_state_idx ON runs(blocked_incident_id, state);

CREATE TABLE IF NOT EXISTS deliveries (
  key TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS deliveries_created_at_idx ON deliveries(created_at);

CREATE TABLE IF NOT EXISTS approvals (
  run_id TEXT NOT NULL,
  sha TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (run_id, sha)
);
CREATE INDEX IF NOT EXISTS approvals_created_at_idx ON approvals(created_at);

CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'available',
  priority INTEGER NOT NULL DEFAULT 0,
  scope_json TEXT NOT NULL DEFAULT '[]',
  source_ref TEXT NOT NULL DEFAULT '',
  worker_agent TEXT NOT NULL DEFAULT '',
  worker_prompt TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS task_deps (
  task_id TEXT NOT NULL,
  depends_on TEXT NOT NULL,
  PRIMARY KEY (task_id, depends_on)
);
CREATE INDEX IF NOT EXISTS task_deps_depends_on_task_idx ON task_deps(depends_on, task_id);

CREATE TABLE IF NOT EXISTS base_incidents (
  id TEXT PRIMARY KEY,
  repo TEXT NOT NULL,
  base_branch TEXT NOT NULL,
  signature TEXT NOT NULL,
  failed_checks_json TEXT NOT NULL DEFAULT '[]',
  fixer_run_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open',
  attempts INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  resolved_at INTEGER NOT NULL DEFAULT 0,
  UNIQUE (repo, base_branch, signature)
);

CREATE TABLE IF NOT EXISTS run_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT '',
  at INTEGER NOT NULL,
  head_sha TEXT NOT NULL DEFAULT '',
  pr_number INTEGER NOT NULL DEFAULT 0,
  reason TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS run_events_run_at_idx ON run_events(run_id, at);
CREATE INDEX IF NOT EXISTS run_events_at_idx ON run_events(at);

CREATE TABLE IF NOT EXISTS decisions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at INTEGER NOT NULL,
  cycle_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL,
  run_id TEXT NOT NULL DEFAULT '',
  task_id TEXT NOT NULL DEFAULT '',
  pr_number INTEGER NOT NULL DEFAULT 0,
  verdict TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '',
  fields_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS decisions_run_at_idx ON decisions(run_id, at);
CREATE INDEX IF NOT EXISTS decisions_kind_at_idx ON decisions(kind, at);
CREATE INDEX IF NOT EXISTS decisions_at_idx ON decisions(at);
` + waitlistDDL

// waitlistDDL holds one row per card waiting on a scope path. waiting_since is
// the age key: a smaller timestamp waited longer, so equal-priority cards keep
// their order. The same DDL runs on a fresh schema and on every open, so an
// older state database gains the table without losing its rows.
const waitlistDDL = `
CREATE TABLE IF NOT EXISTS waitlist (
  path TEXT NOT NULL,
  task_id TEXT NOT NULL,
  waiting_since INTEGER NOT NULL,
  PRIMARY KEY (path, task_id)
);
CREATE INDEX IF NOT EXISTS waitlist_path_age_idx ON waitlist(path, waiting_since, task_id);
`

// dbExecutor is the slice of *sql.DB and *sql.Tx that schema preparation uses.
// Both types satisfy it, so one preparation transaction can run every DDL and
// backfill and the version marker can be stamped in the same commit.
type dbExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ensureWaitlist adds the waitlist table to a state database opened before the
// table existed. It takes the same executor as the other migration steps so it
// runs inside the single preparation transaction.
func ensureWaitlist(ctx context.Context, db dbExecutor) error {
	_, err := db.ExecContext(ctx, waitlistDDL)
	return err
}

// dsn builds a SQLite file URI. The path is escaped as a URL path so a name
// containing ? or # stays part of the filename instead of becoming a query or
// fragment delimiter. The composition root registers the driver under "sqlite".
func dsn(path string) string {
	params := url.Values{}
	params.Add("_pragma", "busy_timeout(5000)")
	params.Add("_pragma", "journal_mode(WAL)")
	params.Add("_pragma", "foreign_keys(ON)")
	// BEGIN IMMEDIATE takes the write lock before the first read in a write
	// transaction. Deferred upgrades can fail with SQLITE_BUSY even after a
	// busy timeout, which parallel control calls hit.
	params.Set("_txlock", "immediate")
	return (&url.URL{
		Scheme:   "file",
		OmitHost: true,
		Path:     path,
		RawQuery: params.Encode(),
	}).String()
}

// schemaObjects names every table, index and trigger the migration creates.
// The version marker alone cannot see an object that an older build created
// outside the marker, so the fast path also checks that all of them exist.
var schemaObjects = []string{
	"runs", "runs_pr_idx", "runs_task_idx", "runs_pr_updated_idx",
	"runs_task_updated_idx", "runs_active_updated_idx", "runs_incident_state_idx",
	"runs_state_updated_idx", "deliveries", "deliveries_created_at_idx",
	"approvals", "approvals_created_at_idx", "tasks", "task_deps",
	"task_deps_depends_on_task_idx", "base_incidents", "run_events",
	"run_events_run_at_idx", "run_events_at_idx", "decisions",
	"decisions_run_at_idx", "decisions_kind_at_idx", "decisions_at_idx",
	"decisions_run_kind_verdict_action_idx", "decisions_orphan_at_idx",
	"decision_summary", "decision_summary_insert", "decision_summary_delete",
	"operation_results", "waitlist", "waitlist_path_age_idx",
}

// schemaPrepared reports whether a database already carries every table,
// index and trigger this file defines. SQLite leaves user_version at zero on a
// new file and bumps schema_version on each DDL statement. openDB stamps the
// final schema_version into user_version only after preparation finishes. The
// object check catches databases that an older build marked prepared before
// operation_results, waitlist or a read index existed.
func schemaPrepared(ctx context.Context, db dbExecutor) (bool, error) {
	var marked, current int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&marked); err != nil {
		return false, err
	}
	if marked == 0 {
		return false, nil
	}
	if err := db.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&current); err != nil {
		return false, err
	}
	if marked != current {
		return false, nil
	}
	holders := strings.TrimSuffix(strings.Repeat("?,", len(schemaObjects)), ",")
	args := make([]any, len(schemaObjects))
	for i, name := range schemaObjects {
		args[i] = name
	}
	var found int
	query := `SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table','index','trigger') AND name IN (` + holders + `)`
	if err := db.QueryRowContext(ctx, query, args...).Scan(&found); err != nil {
		return false, err
	}
	return found == len(schemaObjects), nil
}

// markSchemaPrepared marks a file as prepared from a database handle. Tests
// use it to simulate a database created before a later table existed.
func markSchemaPrepared(db *sql.DB) error {
	return stampSchemaVersion(context.Background(), db)
}

// stampSchemaVersion records the current schema_version in user_version. It
// runs only after all DDL and backfills are in place, and changing
// user_version does not itself change schema_version.
func stampSchemaVersion(ctx context.Context, db dbExecutor) error {
	var current int
	if err := db.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&current); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, current))
	return err
}

func openDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if err := prepareSchema(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// isBusy reports a SQLite lock error. Connection setup and BEGIN can race when
// two processes create the same new file, and the driver returns SQLITE_BUSY
// before its busy timeout takes effect.
func isBusy(err error) bool {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		return coded.Code() == 5 || coded.Code() == 6
	}
	return false
}

// prepareSchema is the only writer of schema DDL. It retries an immediate lock
// error from a new file being created by another opener. A contended lock that
// SQLite waits on through its busy timeout needs no retry.
func prepareSchema(ctx context.Context, db *sql.DB) error {
	const attempts = 20
	var err error
	for i := 0; i < attempts; i++ {
		err = prepareSchemaOnce(ctx, db)
		if err == nil || !isBusy(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return err
}

// prepareSchemaOnce holds the immediate transaction from the version check
// through the final marker, so a second opener waits for the first commit and
// then sees a prepared database. Every DDL and backfill runs in that one
// transaction, including the two tables older builds added outside the marker.
// A failure rolls back the whole migration. The first check stays read only so
// an ordinary CLI open does not take a write lock.
func prepareSchemaOnce(ctx context.Context, db *sql.DB) error {
	prepared, err := schemaPrepared(ctx, db)
	if err != nil {
		return fmt.Errorf("schema version: %w", err)
	}
	if prepared {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("schema transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Re-check under the lock: another opener may have finished while this one
	// waited for it.
	prepared, err = schemaPrepared(ctx, tx)
	if err != nil {
		return fmt.Errorf("schema version: %w", err)
	}
	if prepared {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	if err := ensureRunColumns(ctx, tx); err != nil {
		return fmt.Errorf("runs migration: %w", err)
	}
	if err := backfillRunEvents(ctx, tx); err != nil {
		return fmt.Errorf("run events migration: %w", err)
	}
	if err := dropRedundantRunIndexes(ctx, tx); err != nil {
		return fmt.Errorf("runs indexes: %w", err)
	}
	if err := ensureSnapshotIndexes(ctx, tx); err != nil {
		return fmt.Errorf("snapshot indexes: %w", err)
	}
	if err := ensureDecisionSummary(ctx, tx); err != nil {
		return fmt.Errorf("decision summary: %w", err)
	}
	if err := ensureOperationResults(ctx, tx); err != nil {
		return fmt.Errorf("operation results: %w", err)
	}
	if err := ensureWaitlist(ctx, tx); err != nil {
		return fmt.Errorf("waitlist: %w", err)
	}
	if err := stampSchemaVersion(ctx, tx); err != nil {
		return fmt.Errorf("schema version: %w", err)
	}
	return tx.Commit()
}

// dropRedundantRunIndexes removes runs_state_idx. Its keys are a strict prefix
// of runs_state_updated_idx, which the snapshot indexes supply on every open,
// so keeping it only adds write cost. Databases created before the composite
// index existed still carry it, hence the idempotent drop on open.
func dropRedundantRunIndexes(ctx context.Context, db dbExecutor) error {
	prepared, err := schemaPrepared(ctx, db)
	if err != nil {
		return err
	}
	if prepared {
		return nil
	}
	_, err = db.ExecContext(ctx, `DROP INDEX IF EXISTS runs_state_idx`)
	return err
}

// ensureSnapshotIndexes adds the covering indexes the snapshot reads rely on.
// They live on the read side, next to the queries they serve, so an older
// state database gains them on the next open without a schema version bump.
func ensureSnapshotIndexes(ctx context.Context, db dbExecutor) error {
	prepared, err := schemaPrepared(ctx, db)
	if err != nil {
		return err
	}
	if prepared {
		return nil
	}
	for _, ddl := range []string{
		`CREATE INDEX IF NOT EXISTS runs_state_updated_idx ON runs(state, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS decisions_run_kind_verdict_action_idx ON decisions(run_id, kind, verdict, action)`,
		`CREATE INDEX IF NOT EXISTS decisions_orphan_at_idx ON decisions(run_id, at DESC) WHERE run_id = ''`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	return nil
}

// ensureDecisionSummary keeps a small aggregate of decision counts so a
// snapshot never scans the decision table to build its metrics. A trigger
// maintains the aggregate on every insert; the backfill makes an older state
// database correct on the next open.
func ensureDecisionSummary(ctx context.Context, db dbExecutor) error {
	prepared, err := schemaPrepared(ctx, db)
	if err != nil {
		return err
	}
	if prepared {
		return nil
	}
	const table = `CREATE TABLE IF NOT EXISTS decision_summary (
run_id TEXT NOT NULL, kind TEXT NOT NULL, verdict TEXT NOT NULL, action TEXT NOT NULL,
n INTEGER NOT NULL, PRIMARY KEY (run_id, kind, verdict, action))`
	if _, err := db.ExecContext(ctx, table); err != nil {
		return err
	}
	const insertTrigger = `CREATE TRIGGER IF NOT EXISTS decision_summary_insert
AFTER INSERT ON decisions BEGIN
INSERT INTO decision_summary (run_id, kind, verdict, action, n)
VALUES (NEW.run_id, NEW.kind, NEW.verdict, NEW.action, 1)
ON CONFLICT (run_id, kind, verdict, action) DO UPDATE SET n = n + 1;
END`
	if _, err := db.ExecContext(ctx, insertTrigger); err != nil {
		return err
	}
	// Retention deletes old decisions. Without the matching decrement the
	// summary would keep counting rows that no longer exist, so DecisionCounts
	// would report totals larger than the bounded history it summarizes.
	const deleteTrigger = `CREATE TRIGGER IF NOT EXISTS decision_summary_delete
AFTER DELETE ON decisions BEGIN
UPDATE decision_summary SET n = n - 1
WHERE run_id = OLD.run_id AND kind = OLD.kind
  AND verdict = OLD.verdict AND action = OLD.action;
DELETE FROM decision_summary WHERE run_id = OLD.run_id AND kind = OLD.kind
  AND verdict = OLD.verdict AND action = OLD.action AND n <= 0;
END`
	if _, err := db.ExecContext(ctx, deleteTrigger); err != nil {
		return err
	}
	// Backfill only when the aggregate is empty. A populated summary is kept
	// current by the trigger, so a restart must not regroup a large decision
	// table for no reason.
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM decision_summary`).Scan(&rows); err != nil {
		return err
	}
	if rows > 0 {
		return nil
	}
	_, err = db.ExecContext(ctx, `INSERT INTO decision_summary (run_id, kind, verdict, action, n)
SELECT run_id, kind, verdict, action, COUNT(*) FROM decisions
GROUP BY run_id, kind, verdict, action
ON CONFLICT (run_id, kind, verdict, action) DO UPDATE SET n = excluded.n`)
	return err
}

// backfillRunEvents gives runs saved before the event table existed a start
// and an observation using their existing timestamps. It only inserts into the
// new table and skips runs that already have events, so repeated opens and
// older state databases keep their rows.
func backfillRunEvents(ctx context.Context, db dbExecutor) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO run_events (run_id, kind, state, at, head_sha, pr_number, reason)
SELECT r.id, 'start', 'pending', r.created_at, r.head_sha, r.pr_number, '' FROM runs r
WHERE NOT EXISTS (SELECT 1 FROM run_events e WHERE e.run_id = r.id)
UNION ALL
SELECT r.id, 'observed', r.state, r.updated_at, r.head_sha, r.pr_number, r.reason FROM runs r
WHERE r.updated_at > r.created_at
  AND NOT EXISTS (SELECT 1 FROM run_events e WHERE e.run_id = r.id)`)
	return err
}

// runMigrationColumns are the observation fields added after the initial runs
// table. Each ALTER is guarded by a table_info check so existing state
// databases keep their rows.
var runMigrationColumns = []struct{ name, ddl string }{
	{"observed_base_sha", "observed_base_sha TEXT NOT NULL DEFAULT ''"},
	{"checks_json", "checks_json TEXT NOT NULL DEFAULT '[]'"},
}

// presentColumns reads the column names of table from PRAGMA table_info. It
// closes the rows on a scan error so a failed scan does not hold a connection.
func presentColumns(ctx context.Context, db dbExecutor, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	present := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return nil, err
		}
		present[name] = true
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return present, nil
}

func ensureRunColumns(ctx context.Context, db dbExecutor) error {
	present, err := presentColumns(ctx, db, "runs")
	if err != nil {
		return err
	}
	for _, col := range runMigrationColumns {
		if !present[col.name] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE runs ADD COLUMN `+col.ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

func nowUnix() int64 { return time.Now().UTC().UnixNano() }

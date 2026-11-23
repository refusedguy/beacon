package sqlite

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
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
CREATE INDEX IF NOT EXISTS runs_state_idx ON runs(state);
CREATE INDEX IF NOT EXISTS runs_pr_idx ON runs(pr_number);
CREATE INDEX IF NOT EXISTS runs_task_idx ON runs(task_id);

CREATE TABLE IF NOT EXISTS deliveries (
  key TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS approvals (
  run_id TEXT NOT NULL,
  sha TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (run_id, sha)
);

CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'available',
  priority INTEGER NOT NULL DEFAULT 0,
  scope_json TEXT NOT NULL DEFAULT '[]',
  source_ref TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS task_deps (
  task_id TEXT NOT NULL,
  depends_on TEXT NOT NULL,
  PRIMARY KEY (task_id, depends_on)
);

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
`

func openDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	if err := ensureRunColumns(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("runs migration: %w", err)
	}
	if err := backfillRunEvents(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("run events migration: %w", err)
	}
	return db, nil
}

// backfillRunEvents gives runs saved before the event table existed a start
// and an observation using their existing timestamps. It only inserts into the
// new table and skips runs that already have events, so repeated opens and
// older state databases keep their rows.
func backfillRunEvents(db *sql.DB) error {
	_, err := db.Exec(`
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
}

func ensureRunColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(runs)`)
	if err != nil {
		return err
	}
	present := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		present[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, col := range runMigrationColumns {
		if !present[col.name] {
			if _, err := db.Exec(`ALTER TABLE runs ADD COLUMN ` + col.ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

func nowUnix() int64 { return time.Now().UTC().UnixNano() }

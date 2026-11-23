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
  max_attempts INTEGER NOT NULL DEFAULT 3,
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
	return db, nil
}

func nowUnix() int64 { return time.Now().UTC().UnixNano() }

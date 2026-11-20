-- user_version 6: control-plane state (W1-B).
-- migrate applies this file once and skips it on later opens. Every statement
-- is IF NOT EXISTS, so replaying the body as a first-use guard is harmless.
CREATE TABLE IF NOT EXISTS blocker_incidents (
  id              TEXT PRIMARY KEY,
  repo            TEXT NOT NULL,
  base_ref        TEXT NOT NULL,
  signature       TEXT NOT NULL,
  status          TEXT NOT NULL,
  fixer_watch_id  TEXT NOT NULL DEFAULT '',
  fix_attempts    INTEGER NOT NULL DEFAULT 0,
  max_fix_attempts INTEGER NOT NULL DEFAULT 3,
  payload         BLOB NOT NULL,
  created_at      INTEGER NOT NULL,
  resolved_at     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS blocker_incidents_lookup
  ON blocker_incidents(repo, base_ref, signature, status);

CREATE TABLE IF NOT EXISTS project_orchestrators (
  project_key TEXT PRIMARY KEY,
  session_id  TEXT NOT NULL,
  adapter     TEXT NOT NULL,
  bound_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS runtime_stats (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  payload    BLOB NOT NULL,
  updated_at INTEGER NOT NULL
);

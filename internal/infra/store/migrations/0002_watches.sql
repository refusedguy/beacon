-- user_version 2: активные Watch.
-- TTL как причина закрытия не используется (ADR 0019); закрытие только по
-- терминальному вердикту, отмене или head_changed.
CREATE TABLE watches (
  id       TEXT PRIMARY KEY,
  repo     TEXT NOT NULL,
  pr       INTEGER NOT NULL,
  head_sha TEXT NOT NULL,
  status   TEXT NOT NULL,
  revision INTEGER NOT NULL,
  payload  BLOB NOT NULL
);
CREATE INDEX watches_active ON watches(status, repo, pr, head_sha);

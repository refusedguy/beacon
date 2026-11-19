-- user_version 4: журнал событий. Каждая строка несёт версию схемы (ADR 0012).
CREATE TABLE events (
  seq            INTEGER PRIMARY KEY,
  watch_id       TEXT NOT NULL,
  schema_version INTEGER NOT NULL,
  payload        BLOB NOT NULL,
  created_at     INTEGER NOT NULL
);
CREATE INDEX events_by_watch ON events(watch_id, seq);

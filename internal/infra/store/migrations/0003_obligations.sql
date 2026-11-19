-- user_version 3: append-only журнал обязательств (отчёт G §4, ADR 0015).
-- seq INTEGER PRIMARY KEY без AUTOINCREMENT: строки не удаляются.
-- UNIQUE по тройке даёт идемпотентность повторной доставки.
CREATE TABLE obligations (
  seq             INTEGER PRIMARY KEY,
  id              TEXT NOT NULL UNIQUE,
  watch_id        TEXT NOT NULL,
  revision        INTEGER NOT NULL,
  subscriber_id   TEXT NOT NULL,
  verdict_pointer TEXT NOT NULL,
  delivered_at    INTEGER NOT NULL DEFAULT 0,
  payload         BLOB NOT NULL,
  UNIQUE (watch_id, revision, subscriber_id)
);
CREATE INDEX obligations_by_watch ON obligations(watch_id, seq);
CREATE INDEX obligations_pending ON obligations(delivered_at, seq);

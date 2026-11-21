-- user_version 4: журнал событий. Каждая строка несёт версию схемы (ADR 0012).
CREATE TABLE events (
  seq            INTEGER PRIMARY KEY,
  watch_id       TEXT NOT NULL,
  schema_version INTEGER NOT NULL,
  payload        BLOB NOT NULL,
  created_at     INTEGER NOT NULL
);
-- BCX-38: раньше здесь создавался events_by_watch(watch_id, seq). Append пишет
-- watch_id = scope, поэтому индекс дублировал events_by_scope(scope, seq) из
-- events.go и удваивал запись в индексы на каждое событие. Единственный рабочий
-- индекс events_by_scope создаёт ensureEventLog при открытии базы; он же удаляет
-- унаследованный events_by_watch из уже существующих баз.

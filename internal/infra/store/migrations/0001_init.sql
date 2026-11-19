-- user_version 1: базовая таблица meta.
-- Совпадает с фикстурой corpus (состояние до миграции, ADR 0012).
CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value INTEGER NOT NULL
);

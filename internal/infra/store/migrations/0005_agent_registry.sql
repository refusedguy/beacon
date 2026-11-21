-- user_version 5: project-scoped реестр сабагентов (ADR 0086, T5.21).
-- Три таблицы: записи исполнителей, производный capability index и привязки
-- Watch. project_key обязателен и не бывает пустым: запрос без ключа проекта
-- не должен видеть чужие записи (T5.28). Повторное применение безопасно:
-- migrate пропускает версию <= текущей, а CREATE ... IF NOT EXISTS не ломает
-- прямой повторный запуск SQL.
CREATE TABLE IF NOT EXISTS agent_registry (
  project_key    TEXT NOT NULL CHECK (length(project_key) > 0),
  agent_id       TEXT NOT NULL CHECK (length(agent_id) > 0),
  role           TEXT NOT NULL,
  runtime        TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL DEFAULT 'active',
  session_id     TEXT NOT NULL DEFAULT '',
  notify_adapter TEXT NOT NULL DEFAULT '',
  notify_target  TEXT NOT NULL DEFAULT '',
  config_id      TEXT NOT NULL DEFAULT '',
  digest         TEXT NOT NULL DEFAULT '',
  capabilities   TEXT NOT NULL DEFAULT '[]',
  last_seen      INTEGER NOT NULL DEFAULT 0,
  worktree       TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  PRIMARY KEY (project_key, agent_id)
);

-- Ключ индекса это capability, значение это непрозрачный agent_id (ADR 0089).
-- Один исполнитель может объявлять несколько возможностей.
CREATE TABLE IF NOT EXISTS agent_capabilities (
  project_key TEXT NOT NULL CHECK (length(project_key) > 0),
  capability  TEXT NOT NULL CHECK (length(capability) > 0),
  agent_id    TEXT NOT NULL CHECK (length(agent_id) > 0),
  PRIMARY KEY (project_key, capability, agent_id)
);

-- Один watchID с разными revision даёт разные строки (T5.25). config_id и
-- fencing_token приходят из регистрации вместе с Watch (ADR 0087, 0088).
CREATE TABLE IF NOT EXISTS watch_bindings (
  project_key   TEXT NOT NULL CHECK (length(project_key) > 0),
  watch_id      TEXT NOT NULL,
  revision      INTEGER NOT NULL,
  agent_id      TEXT NOT NULL,
  config_id     TEXT NOT NULL,
  fencing_token INTEGER NOT NULL,
  subscriber_id TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  PRIMARY KEY (watch_id, revision)
);

CREATE INDEX IF NOT EXISTS agent_registry_scope ON agent_registry(project_key, status);
CREATE INDEX IF NOT EXISTS agent_capabilities_by_capability ON agent_capabilities(project_key, capability);
CREATE INDEX IF NOT EXISTS watch_bindings_by_agent ON watch_bindings(project_key, agent_id);

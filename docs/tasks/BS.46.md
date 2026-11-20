# BS.46. Счётчики активных задач, очереди и wake
Статус: done
Фаза: P4
Тип: task
Владелец файлов: internal/infra/store/observability.go, internal/infra/server/metrics.go
Зависимости: BS.45
Ссылка: ADR 0195
Агент: нет
Время: нет
Цель: Измеримая нагрузка сервера.
Контекст: Есть persist_progress, stream_health и wake_observations.
Шаги: Active watches, queue depth, wake latency, executor count, RSS, pointsThisHour, PERSIST_FAILED.
Проверка: Значения совпадают с beacon_stats.
Приёмка: sent и woke остаются независимыми фактами.

## Подтверждение
- PREMISE проверен: функциональность уже в origin/main, код не дублирован и OwnerFiles не тронуты. `internal/infra/store/observability.go` держит persist_progress с PERSIST_FAILED (CodePersistFailed), stream_health и wake_observations, где sent и woke складываются через OR и остаются независимыми фактами (T1.9, T3.19, T3.26). `internal/infra/server/metrics.go` экспонирует RSS (GaugeRSSBytes, FootprintCheck) и низкокардинальные счётчик и гистограмму вызовов без watchID в labels (BS.49, BS.51). Остальные шаги уже в main: `beacon_active_watches` и `beacon_outbox_depth` в `internal/infra/observe/metrics.go` (ActiveWatches, OutboxDepth), задержка wake как `wake_duration_seconds`, executor count через `store.ExecutorCount`, pointsThisHour и activeWatches в `internal/infra/mcp/tool_stats.go`. Проверка "значения совпадают с beacon_stats" зафиксирована в eval e11. Дубля нет, карточку закрываю как реализованную.

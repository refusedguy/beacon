# BS.46. Счётчики активных задач, очереди и wake
Статус: todo
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

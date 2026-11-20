# BS.23. /internal/scheduler route group
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/server/scheduler.go
Зависимости: BS.2, BS.12
Ссылка: ADR 0192
Агент: нет
Время: нет
Цель: Внутренний API планировщика.
Контекст: T3 предоставляет schedule_task/list/delete как часть сервера.
Шаги: CRUD расписаний на loopback под bearer token, валидация выражений и timezone.
Проверка: Внешний вызов без токена недоступен.
Приёмка: Планировщик пишет намерение, а не эффект.

## Подтверждение
- Premise проверен, дубль в код не добавлял. Функциональность уже в main с PR #470 (2a386c4). `internal/infra/server/scheduler.go` содержит CRUD `ScheduleService` (Schedule, List, Pause, Resume, Delete), порт `ScheduleStore` и `SchedulerHandler` на `/internal/scheduler/{schedule,list,pause,delete}`. Хендлер проверяет bearer-токен через `subtle.ConstantTimeCompare` и возвращает 401 без токена. `buildSchedule` проверяет выражения interval, cron и mark и загружает timezone через `time.LoadLocation`. ADR 0189 требует, чтобы планировщик писал намерение и не делал внешний I/O. `Pause` меняет только `Enabled` и не запускает задачу, что проверяет тест `TestSchedulePausePreservesTaskAndDoesNotRun`. CLI-команды `schedule_task/list/delete` подключены в `cmd/beacon/task.go`.

# BS.23. /internal/scheduler route group
Статус: todo
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

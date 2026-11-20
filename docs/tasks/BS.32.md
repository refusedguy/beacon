# BS.32. schedule_task/list/delete через внутренний API
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/server/scheduler.go, cmd/beacon/task.go
Зависимости: BS.23
Ссылка: ADR 0192
Агент: нет
Время: нет
Цель: Управление расписаниями из CLI и внутреннего API.
Контекст: T3 предоставляет schedule_task, list_scheduled_tasks, delete_scheduled_task.
Шаги: Три команды, пауза через enabled=false, удаление постоянное.
Проверка: Пауза сохраняет задачу и не запускает её.
Приёмка: Нет новых MCP-инструментов в первой версии.

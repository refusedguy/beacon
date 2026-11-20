# BS.36. /api/v1 events SSE
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/infra/server/events.go
Зависимости: BS.17
Ссылка: ADR 0194
Агент: нет
Время: нет
Цель: Живой поток событий для TUI.
Контекст: openrig и Crush используют SSE как основной live-канал.
Шаги: Фильтры по проекту и сессии, id: <seq>, data: <json>, heartbeat.
Проверка: Переподключение с Last-Event-ID не дублирует события.
Приёмка: snapshot_required обрабатывается клиентом.

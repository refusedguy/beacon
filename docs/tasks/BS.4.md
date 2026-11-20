# BS.4. /version и /status проекции
Статус: done
Обоснование: функциональность уже в main (PR #615, commit 7994bcc; карточка переведена в done этим же PR). GET /api/v1/health в internal/infra/httpapi/server.go отдаёт version, uptimeSec, isPollerOwner (состояние lease), pid и projectKey; GET /api/v1/snapshot отдаёт LiveWatches (число активных вотчей) и метрики. Оба маршрута объявлены GET в docs/api/openapi.yaml и не пишут в БД, приёмка «только для чтения» выполнена. beacon_stats в internal/infra/mcp/tool_stats.go читает Version и ActiveWatches без записи. Дубля в OwnerFiles internal/infra/server/http.go нет, код не писался.
Фаза: P0
Тип: task
Владелец файлов: internal/infra/server/http.go
Зависимости: BS.2
Ссылка: ADR 0188
Агент: нет
Время: нет
Цель: Базовые read-only проекции сервера.
Контекст: Crush и openrig открывают /version и /status.
Шаги: Версия бинарника, uptime, число активных вотчей, состояние lease.
Проверка: Ответ стабилен и не требует записи в БД.
Приёмка: Маршруты помечены read-only.

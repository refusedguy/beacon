# BS.6. Endpoint writer в connection.json
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/mcp.go, internal/infra/httpapi/client.go
Зависимости: BS.2
Ссылка: ADR 0197
Агент: нет
Время: нет
Цель: Клиент находит сервер без захардкоженного адреса.
Контекст: connection.json уже читается при каждом beacon mcp.
Шаги: Атомарная запись адреса и токена, режим 0600, пере-read на каждом старте.
Проверка: Сервер на новом порту подхватывается следующим клиентом.
Приёмка: Старый файл не ломает чтение.

## Подтверждение
- PR #394 DROPPED: Legacy atomic writer for connection.json TCP-daemon discovery; main has only the reader, mcpConnector is a no-op and MCP is stdio registered by beacon-project plugin; write side never existed.
- Возврат в todo (пересмотр после Waves 6-8, W8-A; низкий приоритет): в main только reader connection.json (cmd/beacon/mcp.go:125) и no-op mcpConnector (:288); writer появился в W8-A. Фаза карточки P0, но приоритет низкий (P3) до влития W8-A.

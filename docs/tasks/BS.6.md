# BS.6. Endpoint writer в connection.json
Статус: dropped
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/mcp.go, internal/infra/server/connection.go
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

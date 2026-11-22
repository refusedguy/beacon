# PROTO-03. Смонтировать аутентифицированный /mcp в один HTTP-сервер
Статус: done
Premise: PR #1481, коммит 8608917dab33456083ef9f510e8b883e1309a02e, подключил authenticated /mcp к общему HTTP-слушателю через ExtraRoutes. git merge-base --is-ancestor 8608917dab33456083ef9f510e8b883e1309a02e origin/main вернул 0.
Фаза: P1
Тип: task
Владелец файлов: internal/infra/server/mcp.go, internal/infra/mcp/client.go
Зависимости: PROTO-02
Ссылка: 
Агент: нет
Время: нет
Цель: Устранить 404 на POST /mcp и оставить один bearer-шлюз.
Контекст: Источник: docs/research/audit-04-protocols.md, таблица строк 3. NewMCPHTTPHandler в internal/infra/server/mcp.go:33-49 проверяет bearer и отдаёт streamable HTTP. handler в internal/infra/httpapi/server.go:713-723 регистрирует только /api/v1/. startHTTPAPI в cmd/beacon/mcp.go:1084-1098 не передаёт MCP-обработчик. Удалённый клиент шлёт запрос на /mcp в internal/infra/mcp/client.go:124. Baseline: 0 смонтированных эндпоинтов.
Шаги: Передать обработчик через конфигурацию HTTP-сервера, добавленную PROTO-02. Не поднимать второй listener. Сохранить проверку токена в константное время и пустой токен как отказ. Обновить комментарий о семи инструментах в internal/infra/server/mcp.go:2.
Проверка: CI зелёный. Тест на tools/list по HTTP совпадает с tools/list через stdio. Тест на 401 без токена. Тест на 200 с токеном.
Приёмка: Изменения только в OwnerFiles карточки. OwnerFiles PROTO-02 не трогаются. Второй HTTP-сервер не создаётся.

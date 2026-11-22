# CSx-06. Интроспекция состояния MCP и динамическое переключение серверов
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/mcp/server.go, internal/infra/mcp/client.go
Зависимости:
Ссылка: docs/research/comp-claude-sdk.md; клон anthropics/claude-agent-sdk-python 3184360
Агент: нет
Время: 2026-11-22
Цель: Показывать состояние подключений MCP и позволять переподключать или отключать сервер без перезапуска агента.
Контекст: SDK задаёт статусы `connected`, `failed`, `needs-auth`, `pending` и `disabled`, а также `McpServerStatus` с информацией о сервере, ошибке и инструментах (`src/claude_agent_sdk/types.py:1023-1060`). Методы `reconnect_mcp_server` и `toggle_mcp_server` меняют состояние через control-запросы (`src/claude_agent_sdk/client.py:395-417`, `_internal/query.py:884-911`), а `get_mcp_status` возвращает снимок (`client.py:471-502`). В beacon список инструментов фиксирован в `toolDescriptors` (`internal/infra/mcp/server.go:130-247`), а адрес единственного удалённого эндпоинта читается один раз (`internal/infra/mcp/client.go:79-97`). CS-05 добавляет локальный мост, но не даёт состояния подключения.
baseline: `rg -n 'get_mcp_status|reconnect_mcp|toggle_mcp|mcp_status' internal cmd` на `origin/main` 7244e15b даёт 0 строк; `rg -c 'Name:' internal/infra/mcp/server.go` даёт 18 строк с объявлениями.
Шаги: Добавить инструмент статуса MCP, который возвращает список серверов с полями `name`, `status`, `error` и `tools`. Добавить инструменты переподключения и отключения с проверкой имени сервера. Хранить состояние в существующем registry, не меняя дескрипторный список доступных инструментов без явного вызова. Повторное отключение делать идемпотентным.
Проверка: Тест подключает два сервера, один из которых отдаёт ошибку, и читает статусы `connected` и `failed`. После отключения второго сервера его инструменты исчезают из tools/list. Измеримая проверка: число записей в статусе равно 2, число инструментов после отключения меньше начального на 1; `rg -n 'get_mcp_status|reconnect_mcp|toggle_mcp' <ветка> -- internal/infra/mcp` даёт не менее 3 строк.
Приёмка: Состояние MCP доступно через инструмент, а переключение не требует перезапуска процесса.

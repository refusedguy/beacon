# CSx-13. Подтверждение получения при прерывании Watch
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/mcp/tool_cancel_watch.go, internal/infra/mcp/server.go
Зависимости:
Ссылка: docs/research/comp-claude-sdk.md; клон anthropics/claude-agent-sdk-python 3184360
Агент: нет
Время: 2026-11-22
Цель: Возвращать вызывающему подтверждение при отмене Watch и проверять поддержку этого подтверждения до рассылки сигнала.
Контекст: SDK перечисляет способность `interrupt_receipt_v1` в метаданных инициализации как признак поддержки подтверждения прерывания (`src/claude_agent_sdk/types.py:1580-1584`). Метод `interrupt` отправляет control-запрос (`src/claude_agent_sdk/_internal/query.py:847-858`, `src/claude_agent_sdk/client.py:308-312`). В beacon `CancelWatchOutput` содержит только поле `ok` (`internal/infra/mcp/tool_cancel_watch.go:41-46`), а событие аудита фиксирует отмену без подтверждения доставки (`:49-58`, `:107-205`). CS-01 проверяет способность хоста для хуков, но не для отмены Watch.
baseline: `rg -n 'interrupt_receipt_v1|receipt' internal/infra/mcp/tool_cancel_watch.go` на `origin/main` 7244e15b даёт 0 строк; `rg -n 'CancelWatchOutput' internal/infra/mcp/tool_cancel_watch.go` даёт 6 строк.
Шаги: Добавить в `CancelWatchOutput` идентификатор подтверждения и признак его поддержки хостом. Проверять способность вызывающего до закрытия Watch. При отсутствии поддержки возвращать явную ошибку вместо тихой отмены. Повторный вызов с тем же Watch возвращает то же подтверждение.
Проверка: Тест отменяет Watch на хосте с поддержкой подтверждения и получает один идентификатор. Повтор возвращает тот же идентификатор. На хосте без поддержки вызов возвращает ошибку и оставляет Watch открытым. Измеримая проверка: `rg -n 'interrupt_receipt_v1|cancelReceipt' <ветка> -- internal/infra/mcp` даёт не менее 3 строк; число закрытых Watch во втором тесте равно 0.
Приёмка: Вызывающий знает, что отмена принята, а старый хост не превращает её в молчаливую потерю сигнала.

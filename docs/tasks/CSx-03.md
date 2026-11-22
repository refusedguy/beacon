# CSx-03. Отложенный вызов инструмента как явный результат
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/mcp/tool_ack.go, internal/domain/types.go
Зависимости:
Ссылка: docs/research/comp-claude-sdk.md; клон anthropics/claude-agent-sdk-python 3184360
Агент: нет
Время: 2026-11-22
Цель: Сохранять отложенный вызов инструмента как данные результата, а не как молчаливый отказ.
Контекст: SDK поддерживает решение `permissionDecision: "defer"` (`src/claude_agent_sdk/types.py:551-558`), останавливает прогон и кладёт вызов в `DeferredToolUse` с полями `id`, `name` и `input` (`types.py:1727-1737`). Поле доступно в `ResultMessage.deferred_tool_use` (`types.py:1782`). В beacon множество решений `ack` закрыто четырьмя значениями `approve`, `reject`, `blocked:not_mine` и `blocked:resolved` (`internal/infra/mcp/tool_ack.go:66-83`), а `WakePayload` несёт только идентификаторы Watch (`internal/domain/types.go:546-551`). CS-02 меняет режимы разрешений, но не вводит отложенный результат. Эта карточка вводит только отложенный вызов.
baseline: `rg -n 'deferred_tool_use|DeferredToolUse' internal cmd` на `origin/main` 7244e15b даёт 0 строк.
Шаги: Добавить решение `defer` в множество решений `ack` без изменения семантики `approve` и `reject`. Записывать отложенный вызов с именем инструмента и входными данными в результат Watch. Сделать повторный `ack defer` идемпотентным по Watch. Не возобновлять агента автоматически: решение остаётся у вызывающего.
Проверка: Тест посылает `ack` с решением `defer` и читает одну запись с именем инструмента и входными данными. Повтор даёт ту же запись без второго цикла. Измеримая проверка: `rg -n 'deferred_tool_use|DecisionDefer' <ветка> -- internal cmd` даёт не менее 2 строк, а число записей в тесте равно 1.
Приёмка: Отложенный вызов доступен вызывающему после остановки прогона и не теряется при повторной доставке.

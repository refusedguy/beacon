# BS.43. MCP HTTP endpoint /mcp
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/server/mcp.go
Зависимости: BS.2, BS.41
Ссылка: ADR 0189
Агент: нет
Время: нет
Цель: Тот же набор инструментов по HTTP.
Контекст: T3 и openrig держат MCP рядом с HTTP-сервером.
Шаги: Async transport go-sdk, тот же NewServerWithDeps, bearer token.
Проверка: tools/list через HTTP совпадает со списком через stdio.
Приёмка: Расхождение имен считается ошибкой контракта.
Premise проверен 2026-11-20, дубль. Функциональность уже в main: коммит 6f4c66f (PR #472) добавил `internal/infra/server/mcp.go` с `MCPPath = "/mcp"` на асинхронном streamable HTTP-транспорте go-sdk, тем же `NewServerWithDeps` и bearer-токеном; `mcp_test.go` проверяет совпадение имен инструментов по HTTP и stdio. Статус done карточка получила в коммите 5574278 (PR #533). Код в OwnerFiles не писал и дубль не создавал.

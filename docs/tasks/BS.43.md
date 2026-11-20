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

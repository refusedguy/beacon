# BS.41. Контракт семи инструментов без изменений
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/infra/mcp/contract_test.go, internal/infra/mcp/server.go
Зависимости: BS.2
Ссылка: ADR 0189
Агент: нет
Время: нет
Цель: Сервер расширяется, контракт не ломается.
Контекст: contract_test требует точный набор имён, OutputSchema и бюджет 64 KiB.
Шаги: Оставить ToolNames, annotations и CycleInstructions прежними; новые возможности REST.
Проверка: contract_test проходит без правки ожидаемых имён.
Приёмка: Любое новое имя инструмента требует отдельного ADR.

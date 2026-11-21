# BCX-69. Убрать второй формат ошибки в internal/infra/mcp
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/mcp/schema.go, internal/infra/mcp/tool_wake.go, docs/adrs/0009-error-handling.md
Зависимости: 
Ссылка: ADR 0009
Агент: нет
Время: нет
Цель: Привести RepairableErrorShape к единому конверту из шести полей.
Контекст: Источник: docs/research/adr-consistency-audit.md, таблица строка 8 и раздел 0009. ADR 0009 предписывает единый error shape из шести полей и запрещает Result[T]. ToErrorShape в schema.go:393 соблюдает контракт, но RepairableErrorShape в tool_wake.go:392 расширяет конверт до девяти полей: retryable, recoveryHint, partial. Второй формат в том же пакете. Базовый замер и приоритет из отчёта: P2.
Шаги: Выбрать: вынести repairable-поля в отдельный контракт с ADR или свести их к шести полям через метаданные. Убрать дубль конверта. Обновить ADR 0009. Связать с BCX-64 по судьбе wake.
Проверка: CI зелёный. Тест проверяет один error shape на границе MCP. ADR описывает оба варианта или их отсутствие.
Приёмка: В internal/infra/mcp нет двух несовместимых форматов ошибки.

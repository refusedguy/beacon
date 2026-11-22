# BCX-69. Убрать второй формат ошибки в internal/infra/mcp
Статус: done
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

Итог: Already landed in PR #1329 (commit 8da24238d8d041b2e84f4f9591f0a785101948da). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1329 merged 2026-11-22T09:03:24+05:00; commit 8da24238d8d041b2e84f4f9591f0a785101948da; git merge-base --is-ancestor 8da24238d8d041b2e84f4f9591f0a785101948da origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1329 8da24238
Время закрытия: 2026-11-22T11:24:28Z

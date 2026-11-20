# ADR 224: Журнал решений и квитанций как источник истины для истории агента

Статус: proposed

## Контекст

В прод-БД events 0 строк, obligations.delivered_at и attempts пусты во всех 15 строках, blocked:not_mine хранит причину и BlockedBy только в in-memory BlockedRegistry MCP.

## Решение

Записывать в events типы wake_staged, wake_delivered, wake_acknowledged, executor_state_changed, watch_blocked, watch_unblocked, orchestrator_assigned с sessionID, obligationID, revision и at. schema_version поднять до 6 с чтением старых строк.

## Последствия

История агента переживает рестарт MCP. Появляется источник для beacon trace и для метрик по решениям. Требуется идемпотентность по паре obligationID и события.

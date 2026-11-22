# ADR 309: ACP как канал доставки wake, AG-UI как источник типизированных SSE-событий

Статус: proposed

## Контекст

Источник: docs/research/audit-04-protocols.md, раздел б. Beacon уже реализовал ACP-клиент в internal/infra/wake/acp.go:425-430,713-776,846-968 с четырьмя раннерами. SSE-транспорт живёт в internal/infra/httpapi/server.go:298-304,899-970 и несёт Type string и Payload json.RawMessage. AG-UI определяет типизированные события с полями type, timestamp, rawEvent, metadata и парой StateSnapshot, StateDelta на JSON Patch RFC 6902 (https://docs.ag-ui.com/concepts/events).

## Решение

ACP оставить каналом хост ↔ агент для доставки wake. Не расширять его до каталога инструментов. Для /api/v1/events заимствовать из AG-UI дискриминированное имя события и типизированную полезную нагрузку, сохранить реплей по Last-Event-ID и версию контракта по образцу beacon.human-decide.v1. Полный AG-UI с отображением Watch в Run не вводить.

## Последствия

Альтернативные ACP-раннеры подключаются заменой команды через ResolveACPRuntimeCommand, новый протокол не нужен. SSE-клиент перестаёт угадывать форму Payload и реже перечитывает весь snapshot. Существующие подписчики сохраняют работу за счёт версии контракта.

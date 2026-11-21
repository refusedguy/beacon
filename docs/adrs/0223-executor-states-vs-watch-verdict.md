# ADR 223: Состояния исполнителя отделены от вердикта вотчины

Статус: proposed

## Контекст

domain.Subscriber хранит только id, role, adapter, target, addedAt, lastActivity. agent_registry.status имеет только active, stale, stopped. parentID сессии есть в opencode.Session, но не попадает в watchRow.

## Решение

Ввести ExecutorState с состояниями working, waiting, done, blocked и полями since, watchID, obligationID, parentSessionID, lastWakeID, lastAckID. Хранить в отдельной таблице executor_states, не расширяя BLOB Watch.

## Последствия

Состояние-executor перестаёт выводиться из вердикта. Нужна миграция и явные переходы, чтобы done не путалось с остановкой вотчины.

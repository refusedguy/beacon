# W01_direct_wake_steer

## Сценарий

Сессия ses_123 припаркована. Приходит событие keep_working с новым фактом. Orchestrator вызывает POST /api/session/ses_123/prompt с text и delivery steer.

## Проверки

- HTTP 200 и data.delivery = steer
- События session.inbox.enqueued затем session.execution.started
- Один beacon_ack без HTTP-вызова сессию не будит

## Негативный контроль

Тот же вызов с delivery queue и без PATCH: execution.started не появляется, сессия остаётся idle.

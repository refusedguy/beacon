# W07_cursor_steer

## Сценарий

Cursor steer. Ответ 202 с delivery steered и runId, затем SSE status RUNNING или nudge.delivered.

## Проверки

- Ответ 202 содержит delivery steered и runId
- SSE показывает status RUNNING или приходит nudge.delivered
- При 409 agent_busy receipt показывает причину

## Негативный контроль

Вызвать обычный follow-up на активном run. При 409 agent_busy wake не происходит.

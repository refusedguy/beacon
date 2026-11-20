# R01_registration_idempotent

## Сценарий

Два вызова watch_pr с одним executor и revision

## Проверки

- Одна запись agent_registry
- Одна строка watch_bindings
- revision не увеличивается

## Негативный контроль

Третий вызов с другим sessionID создаёт вторую запись

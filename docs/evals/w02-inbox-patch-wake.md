# W02_inbox_patch_wake

## Сценарий

Задача с текстом Recheck CI отправлена с delivery queue, ответ дал msg_456. Позже на событие ready выполняется PATCH inbox на steer.

## Проверки

- GET inbox показывает msg_456 с delivery queue
- PATCH с delivery steer возвращает 204
- Приходят session.inbox.delivery.changed и затем session.execution.started

## Негативный контроль

PATCH с delivery queue оставляет задачу припаркованной и не меняет статус выполнения.

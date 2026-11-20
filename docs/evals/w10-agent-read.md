# W10_agent_read

## Сценарий

Прочтение агента. После wake агент вызывает beacon_get_result и beacon_ack, в store появляется readAt.

## Проверки

- После wake агент вызывает beacon_get_result и beacon_ack
- В store появляется readAt
- read ставится выдачей содержимого через beacon_get_result

## Негативный контроль

Удалить адаптер. Без адаптера readAt нет, через 15 минут поднимается эскалация.

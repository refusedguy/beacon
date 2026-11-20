# E01_intent_receipt

## Сценарий

Отправить команду follow и сразу оборвать соединение, затем повторить с тем же clientRequestId.

## Проверки

- В events одна запись команды
- Повтор возвращает тот же receipt
- effect_status отделён от intent_committed

## Негативный контроль

Повтор без clientRequestId создаёт вторую команду.

# W14_durable_obligation_restart

## Сценарий

Создать обязательство с destination, симулировать crash daemon до wake и поднять daemon заново.

## Проверки

- Строка остаётся в pending
- Wake intent из транзакции обнаружен и доставлен один раз
- Claim отсутствует

## Негативный контроль

Убрать запись wake intent из транзакции и проверить, что работа застревает без finding.

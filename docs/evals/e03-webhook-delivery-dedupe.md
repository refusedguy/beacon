# E03_webhook_delivery_dedupe

## Сценарий

Две доставки с одинаковым X-GitHub-Delivery и событие со старым headSHA.

## Проверки

- revision Watch меняется только один раз
- obligation не дублируется
- устаревшее событие не меняет вердикт

## Негативный контроль

Receiver без дедупликации создаёт две obligation и два wake.

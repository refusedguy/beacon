# E08_webhook_delivery_dedupe

## Сценарий

Доставить одно событие дважды и вручную redelivery.

## Проверки

- одна строка webhook_events
- один refresh
- revision изменился один раз

## Негативный контроль

handler без UNIQUE создаёт два wake

# E03_delivery_receipt_persisted

## Сценарий

beacon deliver отправляет wake в живую сессию и получает ответ 200.

## Проверки

- delivered_at больше нуля
- attempts содержит одну запись с исходом posted
- повторная доставка не создаёт вторую запись с тем же номером попытки

## Негативный контроль

Транспортная ошибка оставляет delivered_at нулевым и пишет outcome transport-failed.

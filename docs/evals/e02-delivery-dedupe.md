# E02_delivery_dedupe

## Сценарий

Две доставки с одинаковым X-GitHub-Delivery плюс ручной redelivery того же события.

## Проверки

- журнал webhook_events содержит одну строку на delivery_id
- revision Watch меняется один раз
- .obligation не дублируется и wake вызывается один раз
- повтор возвращает сохранённый outcome

## Негативный контроль

Receiver без дедупликации создаёт две obligation и два wake на один Watch.

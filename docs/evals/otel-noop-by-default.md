# OTEL_NOOP_BY_DEFAULT

## Сценарий

Запуск beacon без BEACON_OTEL_ENABLED и с BEACON_OTEL_ENABLED=1.

## Проверки

- Start возвращает исходный контекст и не устанавливает SpanContext
- Recorder не получает ни одного спана
- нагрузка не требует коллектора и не добавляет зависимостей в go.mod

## Негативный контроль

Сравнить с BEACON_OTEL_ENABLED=true: в этом режиме спан появляется.

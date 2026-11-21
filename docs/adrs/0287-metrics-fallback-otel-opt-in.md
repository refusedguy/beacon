# ADR 287: /metrics как fallback, OTel по явному opt-in

Статус: proposed

## Контекст

У Beacon есть durable-счётчики, но нет стандартных метрик и correlation ID; зависимости OTel отсутствуют.

## Решение

/metrics отдаёт Prometheus-формат без тяжёлых зависимостей. OTLP включается только при OTEL_EXPORTER_OTLP_ENDPOINT.

## Последствия

Базовый запуск остаётся лёгким. Метрики не приписывают вердикту время исполнения.

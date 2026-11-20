# BS.45. /metrics в Prometheus формате
Статус: done
Фаза: P4
Тип: task
Владелец файлов: internal/infra/server/metrics.go
Зависимости: BS.2, BS.14
Ссылка: ADR 0195
Агент: нет
Время: нет
Цель: Стандартные метрики без тяжёлых зависимостей.
Контекст: otel-observability.md предлагает promhttp.HandlerFor как fallback.
Шаги: Registry, /metrics, счётчики и observable gauges.
Проверка: Скрэп видит beacon_active_watches и beacon_outbox_depth.
Приёмка: Метрики низкой кардинальности.

## Подтверждение
- PREMISE MET: функциональность уже в origin/main. internal/infra/observe/metrics.go содержит реестр, эндпоинт /metrics, счётчики и gauge beacon_active_watches и beacon_outbox_depth с фиксированной кардинальностью. cmd/beacon/observe.go поднимает этот эндпоинт. internal/infra/server/metrics.go тоже экспортирует метрики в формате Prometheus без тяжёлых зависимостей. Дубль не писан, код не менялся.

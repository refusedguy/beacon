# BS.45. /metrics в Prometheus формате
Статус: done
Premise: проверено 2026-11-20, дубль. Функциональность уже в main: PR #420 (BS.45: Prometheus /metrics endpoint and gauges, MERGED). `internal/infra/observe/metrics.go` содержит `Metrics.Prometheus()` в Prometheus text format, `Metrics.Handler()` и `StartMetricsServer()` с маршрутом `GET /metrics` на loopback, а также безлабельные gauges `beacon_active_watches` (`GaugeActiveWatches`) и `beacon_outbox_depth` (`GaugeOutboxDepth`) с сеттерами `ActiveWatches`/`OutboxDepth`. Проверка скрэпа зафиксирована в `cmd/beacon/observe_test.go`. OwnerFiles `internal/infra/server/metrics.go` байт-в-байт совпадает с origin/main и уже экспортирует через `Metrics.Prometheus()` счётчик `beacon_call_total`, гистограмму `beacon_call_duration_seconds` и gauge `beacon_rss_bytes` с фиксированной кардинальностью. Код не писал, OwnerFiles не трогал, дубль не создан.
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

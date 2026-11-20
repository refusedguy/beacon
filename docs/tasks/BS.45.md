# BS.45. /metrics в Prometheus формате
Статус: todo
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

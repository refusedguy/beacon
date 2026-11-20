# ADR 202: Structured JSON logs и Prometheus /metrics как fallback без OTel

Статус: accepted

## Контекст

OTel добавляет зависимости и коллектор. Crush обходится локальным файлом логов и метриками с device hash, OpenRig вообще не шлёт телеметрию из ядра. client_golang даёт promhttp.HandlerFor, CounterVec, HistogramVec и встроенные collectors.

## Решение

Держать slog.JSONHandler всегда. При отсутствии OTEL_EXPORTER_OTLP_ENDPOINT поднимать promhttp.Handler на 127.0.0.1:9090 с теми же семью счётчиками, Go- и process-коллекторами. OTel подключается позже без изменения контракта метрик.

## Последствия

Теряется корреляция между MCP, daemon и wake-адаптером и дерево спанов. Остаются durable-журнал, beacon_stats и простые счётчики. Публичный порт не открывается, второй credential store не вводится.

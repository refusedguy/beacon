# ADR 200: OTel как opt-in поверх существующей наблюдаемости store

Статус: accepted

## Контекст

У Beacon уже есть persist_progress, stream_health, wake_observations и beacon_stats, но нет корреляции между процессами. go.mod не содержит opentelemetry. t3code держит локальный NDJSON как источник истины и включает OTLP только по env, Crush пишет локальный лог, OpenRig не экспортирует ядро.

## Решение

Добавить internal/infra/telemetry с TracerProvider, MeterProvider и LoggerProvider. Экспорт OTLP включать наличием OTEL_EXPORTER_OTLP_ENDPOINT. Локальный NDJSON и slog.JSONHandler доступны всегда. Все провайдеры закрываются Shutdown. beacon_stats и таблицы store не меняются.

## Последствия

Vendor-neutral конвейер, стандартные семконвы, но новая зависимость и расход на экспорт. HTTP/protobuf на 4317/4318 по умолчанию. OTEL_SDK_DISABLED=true глушит экспорт. File exporter не делает fsync, часть буфера может потеряться при падении хоста.

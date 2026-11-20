# BS.47. OTel спаны по opt-in
Статус: done
Фаза: P4
Тип: task
Владелец файлов: internal/infra/otel/traces.go
Зависимости: BS.45
Ссылка: ADR 0195
Агент: нет
Время: нет
Цель: Распределённая трассировка без обязательных зависимостей.
Контекст: go.mod не содержит OTel; включать только при OTEL_EXPORTER_OTLP_ENDPOINT.
Шаги: Спаны JSON-RPC, ci.refresh, daemon.tick, orchestrator.merge, wake.deliver.
Проверка: Без endpoint нет экспорта и лишних зависимостей.
Приёмка: Kill switch OTEL_SDK_DISABLED работает по спецификации.

## Подтверждение
- PR #394 MERGED: 2026-11-20 (squash).

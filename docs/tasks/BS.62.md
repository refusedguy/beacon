# BS.62. Конфиг merge.driver
Статус: dropped
Фаза: P5
Тип: task
Владелец файлов: internal/infra/registry/config.go, internal/app/orchestrator/orchestrate.go
Зависимости: BS.8, BS.57
Ссылка: ADR 0201
Агент: нет
Время: нет
Цель: Выбор между Beacon и GitHub auto-merge.
Контекст: waitFor и waitForMain не выражаются GraphQL-мутацией.
Шаги: Значения beacon и github-auto-merge, ошибка конфига при waitFor с auto-merge.
Проверка: При waitFor драйвер принудительно beacon.
Приёмка: requireApproval и requireUpToDate совпадают с branch protection.

## Подтверждение
- PR #394 DROPPED: Configurable merge.driver (Beacon vs GitHub auto-merge) removed by new contract: Beacon auto-merge only (ADR 0072); waitFor/waitForMain are policy exceptions, not a driver choice.

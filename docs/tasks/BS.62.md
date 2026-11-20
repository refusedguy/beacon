# BS.62. Конфиг merge.driver
Статус: todo
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

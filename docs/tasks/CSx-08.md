# CSx-08. Событие лимита с утилизацией и временем сброса
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/domain/types.go, internal/app/orchestrator/workload.go
Зависимости:
Ссылка: docs/research/comp-claude-sdk.md; клон anthropics/claude-agent-sdk-python 3184360
Агент: нет
Время: 2026-11-22
Цель: Дать планировщику beacon сигнал о приближении к лимиту до того, как вызов будет отвергнут.
Контекст: SDK описывает `RateLimitInfo` со статусами `allowed`, `allowed_warning` и `rejected`, долей использованного лимита `utilization`, временем сброса `resets_at` и типом окна (`src/claude_agent_sdk/types.py:1847-1878`). `RateLimitEvent` приходит при смене состояния и позволяет заранее уменьшить темп (`types.py:1881-1892`). В beacon `CodeRateLimited` это только код ошибки HTTP 429 (`internal/domain/types.go:655-691`), а проверка `rate_limit` в `cmd/beacon/mcp.go:1700-1702` читает счётчик GitHub с трёхсекундным дедлайном. CS-02 меняет разрешения на вызов инструмента, но не планирует спрос по лимиту.
baseline: `rg -n 'rate_limit_info|RateLimitEvent|utilization' internal cmd` на `origin/main` 7244e15b даёт 0 строк; `rg -n 'CodeRateLimited' internal/domain/types.go` даёт 2 строки.
Шаги: Добавить тип события лимита со статусом, утилизацией и `resets_at`. Подключить событие к планировщику спавна в `internal/app/orchestrator/workload.go`. При `allowed_warning` откладывать новый spawn до `resets_at`, при `rejected` не запускать новые агенты. Сохранить HTTP 429 как отдельный путь ошибки вызова.
Проверка: Тест подаёт событие со статусом `allowed_warning` и утилизацией 0.8. Планировщик откладывает один spawn и не отвергает уже идущие вызовы. Измеримая проверка: число отложенных spawn равно 1, число отвергнутых вызовов равно 0; `rg -n 'RateLimitEvent|rate_limit_info' <ветка> -- internal` даёт не менее 2 строк.
Приёмка: Планировщик реагирует на предупреждение до наступления жёсткого лимита.

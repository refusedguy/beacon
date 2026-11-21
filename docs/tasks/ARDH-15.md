# ARDH-15. Убрать импорты infra из слоя app
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/app/orchestrator/orchestrator.go, internal/app/orchestrator/daemon.go, internal/app/orchestrator/store.go, internal/app/session/registry.go, internal/app/command/processor.go, internal/app/taskcli/service.go, cmd/beacon/mcp.go
Зависимости: 
Ссылка: ADR 0008
Агент: нет
Время: нет
Цель: Определить нужные интерфейсы в пакетах потребителя и перенести адаптеры observe, store, taskstore и channel в cmd/beacon.
Контекст: Источник: docs/research/solid-gof-audit.md, ось DIP. Шесть файлов импортируют конкретные пакеты infra в продуктивном коде. app/command одновременно объявляет Outbox и несёт адаптер к channel. Риск: средний. Метрики observe можно оставить явным исключением с пояснением.
Шаги: По одному импорту за шаг объявить порт в app и перенести адаптер. Проверить layers gate и CI после каждого шага.
Проверка: matrix слоёв не изменилась, build и test в CI зелёные.
Приёмка: Продуктивный код app не импортирует конкретные infra, адаптеры собраны в cmd/beacon или явно обоснованы.

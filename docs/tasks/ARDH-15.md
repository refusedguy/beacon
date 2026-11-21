# ARDH-15. Убрать импорты infra из слоя app
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/app/orchestrator/orchestrator.go, internal/app/orchestrator/daemon.go, internal/app/orchestrator/store.go, internal/app/session/registry.go, internal/app/command/processor.go, internal/app/taskcli/service.go, cmd/beacon/mcp.go
Зависимости: 
Ссылка: ADR 0008
Агент: нет
Время: нет
Цель: Определить нужные интерфейсы в пакетах потребителя и перенести адаптеры observe, store, taskstore и channel в cmd/beacon.
Контекст: Источник: docs/research/solid-gof-audit.md, ось DIP. Шесть файлов импортируют конкретные пакеты infra в продуктивном коде. app/command одновременно объявляет Outbox и несёт адаптер к channel. Риск: средний. Метрики observe можно оставить явным исключением с пояснением.
baseline: 9 продуктовых импортов internal/infra/* в internal/app до правки (rg internal/app, 2026-11-21). После этого PR 7. Матрица слоёв не изменилась. Ребро internal/app -> internal/infra остаётся разрешённым.
Шаги: По одному импорту за шаг объявить порт в app и перенести адаптер. Проверить layers gate и CI после каждого шага.
Проверка: matrix слоёв не изменилась, build и test в CI зелёные.
Приёмка: Продуктивный код app не импортирует конкретные infra, адаптеры собраны в cmd/beacon или явно обоснованы.

## Консервативная часть в этом PR

- internal/app/command. Порт `ChannelCommand` объявлен в потребителе. Адаптер `channelCommandSink` перенесён в cmd/beacon/channel_sink.go и реализует `channel.CommandSink`. Формат JSON эффекта не изменился, поэтому `channelApplySink` разбирает тот же payload.
- internal/app/orchestrator/store.go. Адаптер `DBStore` перенесён в cmd/beacon/orchestrator_store.go как `dbOrchestratorStore`. В app остались порты `Store`, `WatchHolder`, `MergedPRReader` и `WatchReader`. Тесты адаптера перенесены в cmd/beacon/orchestrator_store_test.go.
- Импорты observe в orchestrator.go, daemon.go и reminders_loop.go помечены как явное исключение по условию карточки. Поведение метрик не менялось.

## Todo с причиной

- internal/app/orchestrator/daemon.go, импорт wake. `NewDaemonWaker` собирает `wake.T211AdapterRegistry` и регистрирует `daemonWakeAdapter`. Перенос адаптера меняет сигнатуру конструктора и задевает тесты wake в app и cmd. Правка несёт риск для доставки wake, а локально доступен только gofmt. Нужен отдельный PR с прогоном CI.
- internal/app/session/registry.go, импорт store. Порт `SessionStore` использует тип `store.Session` в сигнатурах. Нужны локальный тип `Session`, адаптер в cmd и правка фейков в registry_test.go, daemon_touch_test.go, attach_resume_test.go и spawn_registry_test.go. Шаг отложен по той же причине. Правка задевает много точек вызова, а локальная проверка ограничена gofmt.
- internal/app/taskcli/service.go и internal/app/tasks/reminders.go, импорт taskstore. taskcli использует LoadCards, Card, LockPath и другие функции пакета как библиотеку. В tasks порты и типы объявлены в taskstore, адаптер `ReminderStore` живёт там же. Перенос требует перевода адаптера в cmd и правки его тестов. Шаг отложен до отдельного PR с прогоном CI.

После этого PR выполнен `gofmt -w` на изменённых файлах .go. Команды `go test`, `go build` и gates не запускались локально по правилам AGENTS.md. Окончательная проверка в CI.

Итог: Уже влито в PR #1177
Доказательство: 6b7ce52a ancestor of origin/main
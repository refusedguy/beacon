# WIRING-489. Подключить напоминания о дедлайнах
Premise: проверено 2026-11-21. Ветка от origin/main 152b013, замеры снимались на 5932a62. PR 489 влит, но после него в дереве лежали только чистый Plan, Notifier и тесты в internal/app/tasks. Адаптера store и периодического вызова Notify не было. Матрица `tools/beacon-gates/gate/config/layers.json` запрещает ребра `internal/infra -> internal/app` и `internal/app -> internal/app`. Порт перенесён в `internal/infra/taskstore`, app-пакет алиасирует его типы, цикл живёт в `internal/app/orchestrator` и принимает `NotifyFunc`, а `cmd/beacon/daemon.go` собирает адаптер и нотификатор. Ребро `internal/app -> internal/infra` и `cmd -> internal/app` в матрице есть, новых ребер правка не добавляет.
Статус: done
baseline: замер 2026-11-21 на 5932a6298790173c593235dc8c6b6283447f0c7a, бака ветки 152b01337d1cd784f2530b69f66342a58f54cdd0. До правки `git grep -n -E 'ReminderPorts|NewNotifier' 5932a62 -- internal cmd` давал только `internal/app/tasks/reminders.go` и `internal/app/tasks/reminders_test.go`. `rg -n '"github.com/refusedguy/beacon/internal/app/' internal/app --glob '*.go'` пуст, поэтому ребро `internal/app -> internal/app` в матрице не используется. `cat tools/beacon-gates/gate/config/layers.json` содержит `internal/app -> internal/infra` и `cmd -> internal/app`, но не содержит `internal/infra -> internal/app` и `internal/app -> internal/app`.
Фаза: P0
Тип: task
Владелец файлов: internal/infra/taskstore/reminders.go, internal/infra/taskstore/reminders_test.go, internal/app/orchestrator/reminders_loop.go, internal/app/orchestrator/reminders_loop_test.go, internal/app/tasks/reminders.go, cmd/beacon/daemon.go, cmd/beacon/reminders_test.go, docs/tasks/WIRING-489.md
OwnerFiles: internal/infra/taskstore/reminders.go, internal/infra/taskstore/reminders_test.go, internal/app/orchestrator/reminders_loop.go, internal/app/orchestrator/reminders_loop_test.go, internal/app/tasks/reminders.go, cmd/beacon/daemon.go, cmd/beacon/reminders_test.go, docs/tasks/WIRING-489.md
Зависимости: PR 489
Ссылка: PR 489
SourceWatch: watch:refusedguy/beacon:489:d106ad77ea40d197a1f380839e5462777728ce81
PR: 489
Агент: executor
Время: нет
Цель: Подключить отложенную часть фичи после мержа PR 489.
Контекст: PR 489 добавляет чистый Plan и Notifier в internal/app/tasks. Порт ReminderPorts не имел адаптера в store, и никто не вызывал Notify по расписанию. OwnerFiles расширены владельцем, код написан.
Шаги: Подключить адаптер в infra, перенести типы порта в infra и алиасировать их в app, добавить периодический цикл и wiring в daemon.
Проверка: Адаптер реализует порт, цикл вызывает Notify по тикеру, тесты на фейковом порту и фейковом NotifyFunc проходят в CI, слои не ломаются.
Приёмка: Соответствующий CI зелёный, долг `beacon deferred` переведён в done.

## Причина

Адаптер порта ReminderPorts читает задачи из store, а периодический вызов Notify живёт в daemon. Изначально оба файла входили в OwnerFiles других агентов, поэтому проводка была вынесена отдельно. Владелец расширил OwnerFiles, блокер снят.

## Решение слоёв

Матрица ставит `internal/infra` на ранг 1, `internal/app` на ранг 2, `cmd` на ранг 3. Разрешены ребра `app -> infra` и `cmd -> app`. Ребра `infra -> app` и `app -> app` отсутствуют. Прямая реализация `tasks.ReminderPorts` в `internal/infra/taskstore` потребовала бы запрещённого импорта `internal/app/tasks`. Поэтому интерфейс и его два типа данных определены в `internal/infra/taskstore`, а `internal/app/tasks` объявляет алиасы `Task`, `Reminder` и `ReminderPorts`. Чистая логика `Plan` и `Notifier` осталась в app. Тикер не импортирует соседний app-пакет: `internal/app/orchestrator/reminders_loop.go` принимает `NotifyFunc`. Композиция живёт в `cmd/beacon/daemon.go`, который импортирует и app, и infra. Матрица не менялась.

Канал доставки в карточке не был задан. Адаптер отправляет каждое напоминание структурированным событием `deadline_reminder` через `internal/infra/observe`, то есть в настроенный лог и метрики. Новый транспорт и схема БД не нужны. Дедупликация идёт по ключу задачи и дедлайна в адаптере и в ledger нотификатора.

## Подтверждение

2026-11-21. Реализовано на ветке `fix/w489-reminders` от origin/main 152b013. Создан адаптер `taskstore.ReminderStore` с внедрёнными `ReminderLoad` и `ReminderDeliver`, он подавляет повтор по ключу `ReminderKey`. `internal/app/tasks/reminders.go` алиасирует порт и типы из infra. `internal/app/orchestrator/reminders_loop.go` запускает тикер с интервалом по умолчанию одна минута. `cmd/beacon/daemon.go` собирает нотификатор над store-адаптером, запускает цикл в горутине и делает один проход при `--once`. Тесты: `internal/infra/taskstore/reminders_test.go` на фейковом deliver, `internal/app/orchestrator/reminders_loop_test.go` на фейковом `NotifyFunc`, `cmd/beacon/reminders_test.go` на фейковом порте. Локально применён только `gofmt`; `go test` и `go build` оставлены CI. Ссылка на PR: [#1156](https://github.com/refusedguy/beacon/pull/1156).

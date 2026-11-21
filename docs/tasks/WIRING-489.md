# WIRING-489. Подключить напоминания о дедлайнах
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: internal/infra/taskstore/reminders.go, internal/app/orchestrator/reminders_loop.go
OwnerFiles: internal/infra/taskstore/reminders.go, internal/app/orchestrator/reminders_loop.go
Зависимости: PR 489
Ссылка: PR 489
SourceWatch: watch:refusedguy/beacon:489:d106ad77ea40d197a1f380839e5462777728ce81
PR: 489
Агент: нет
Время: нет
Цель: Подключить отложенную часть фичи после мержа PR 489.
Контекст: PR 489 добавляет чистый Plan и Notifier в internal/app/tasks. Порт ReminderPorts пока не имеет адаптера в store, и никто не вызывает Notify по расписанию. Оба файла принадлежат другим агентам, поэтому проводка вынесена отдельно.
Шаги: Создать карточку, подключить часть фичи, закрыть долг.
Проверка: Проводка подключена, карточка лежит в PR.
Приёмка: Соответствующий CI зелёный, долг `beacon deferred` переведён в done.

## Причина

Адаптер порта ReminderPorts читает задачи из store, а периодический вызов Notify живёт в daemon. Оба файла входят в OwnerFiles других агентов и выходят за скоуп BS.65. В PR 489 вошла только чистая логика, порт и тесты.

## Блокер

- 2026-11-21, проверка premise: PR 489 влит коммитом `9d1a480`, но после `git fetch origin/main` ветка осталась на `6e8f1fd` и не содержит OwnerFiles; вызовов `tasks.NewNotifier` вне тестов нет. Адаптер в `internal/infra/taskstore/reminders.go` требует импорта `internal/app/tasks`, а матрица `tools/beacon-gates/gate/config/layers.json` запрещает ребро `internal/infra` -> `internal/app`. Запуск периодического цикла живёт в `cmd/beacon/daemon.go`. Матрица и daemon находятся вне OwnerFiles. В `taskstore.Card` нет поля дедлайна, канал доставки напоминаний не задан, а дедлайн Watch уже обрабатывает `internal/domain/verdict.go:312`. Статус остаётся todo до решения владельца о расширении OwnerFiles.

## Шаги

1. Дождаться мержа PR 489.
2. Реализовать ReminderPorts в `internal/infra/taskstore/reminders.go`: запрос задач с дедлайном не позже переданного instant и идемпотентная отправка напоминания по ключу задачи и дедлайна.
3. Добавить периодический вызов `NewNotifier(ports, time.Now).Notify(ctx)` в `internal/app/orchestrator/reminders_loop.go`.
4. Вызвать `beacon deferred done` для записи с `watchID` из SourceWatch.

## Проверка

- Список OwnerFiles совпадает с полем «Владелец файлов».
- Карточка `docs/tasks/WIRING-489.md` попала в PR.
- Локальный гейт `WIRING-*` проходит: статус `todo`, OwnerFiles заполнен, `## Причина` есть.

## Приёмка

Проводка применена, запись в `beacon deferred --format json` имеет `status: done`, CI зелёный.

## Подтверждение

2026-11-21 перепроверено по свежему origin/main (ba64d1e; база ветки fix/al7 до правки была ba64d1e, совпадает с origin/main). PR [#489](https://github.com/refusedguy/beacon/pull/489) влит коммитом 9d1a480. Он внёс только чистый Plan, Notifier и тесты к ним. `git grep -n -E 'ReminderPorts|NewNotifier|\.Notify\(' ba64d1e -- internal cmd docs/tasks` показывает ReminderPorts и NewNotifier только в `internal/app/tasks/reminders.go` и `internal/app/tasks/reminders_test.go`. Остальные совпадения `.Notify(` относятся к notify.Router, wake и session. К напоминаниям о дедлайнах они не относятся. Файлов `internal/infra/taskstore/reminders.go` и `internal/app/orchestrator/reminders_loop.go` на свежей main нет. `git log --all -- internal/infra/taskstore/reminders.go internal/app/orchestrator/reminders_loop.go` пуст. `git log 591c023..origin/main` содержит только docs-коммиты, включая 7c7e5c2 и ba64d1e. Реализацию он не принёс. `gh pr list --state all --search 'reminder OR ReminderPorts OR 489'` показывает PR 489 и docs-карточки 821, 989, 897, 1006 и 1016. Реализующего PR после 489 нет. Матрица `tools/beacon-gates/gate/config/layers.json` ставит `internal/infra` на ранг 1, а `internal/app` на ранг 2. Разрешено ребро `internal/app` -> `internal/infra`, обратное ребро запрещено. Адаптер в `internal/infra/taskstore/reminders.go` не может импортировать `internal/app/tasks`. Периодический вызов Notify живёт в `cmd/beacon/daemon.go`, который не входит в OwnerFiles карточки. В `taskstore.Card` поля дедлайна нет. Дедлайн Watch обрабатывает `internal/domain/verdict.go` через `DeadlineAt` и `timedOut`. Канал доставки напоминаний не задан. Блокер описывает актуальное состояние. Статус остаётся todo до решения владельца о расширении OwnerFiles. Код не писал, gofmt не применял, чужие файлы, wiring и .beacon/ не трогал, правка только текстовая.

<!-- wiring-card:contract
beacon_defer_wiring записывает отложенную проводку в durable store и добавляет
эту карточку в тот же PR. Файловый маркер больше не входит в контракт: гейт
читает только карточки docs/tasks/WIRING-*.md. Старый файл
.beacon/wiring-pending, если он остался в дереве, гейт игнорирует.
Карточка без "Статус: todo", пустой OwnerFiles или отсутствующая "## Причина"
проваливает проверку.
wiring-card:contract -->

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

2026-11-21 перепроверено по свежему origin/main (c716b61; база ветки fix/ai5 была 11ce87e). PR [#489](https://github.com/refusedguy/beacon/pull/489) влит коммитом 9d1a480, но внёс только чистый Plan и Notifier и тесты к ним. Проводка не выполнена: `git grep -E 'NewNotifier|ReminderPorts|\.Notify\('` по origin/main в `internal/infra/taskstore` и `internal/app/orchestrator` не дал ни одной строки, файла `reminders_loop.go` нет. Причина остаётся прежней и подтверждена заново. Матрица `tools/beacon-gates/gate/config/layers.json` разрешает ребро `internal/app` -> `internal/infra`, но не наоборот, поэтому адаптер в `internal/infra/taskstore/reminders.go` не может импортировать `internal/app/tasks`. Периодический вызов Notify живёт в `cmd/beacon/daemon.go`, который не входит в OwnerFiles карточки. В `taskstore.Card` нет поля дедлайна, а дедлайн Watch уже обрабатывает `internal/domain/verdict.go` через `DeadlineAt` и `timedOut`. Канал доставки напоминаний не задан. Статус остаётся todo до решения владельца о расширении OwnerFiles. Код не писал, gofmt не применял, чужие файлы, wiring и .beacon/ не трогал, правка только текстовая.

<!-- wiring-card:contract
beacon_defer_wiring записывает отложенную проводку в durable store и добавляет
эту карточку в тот же PR. Файловый маркер больше не входит в контракт: гейт
читает только карточки docs/tasks/WIRING-*.md. Старый файл
.beacon/wiring-pending, если он остался в дереве, гейт игнорирует.
Карточка без "Статус: todo", пустой OwnerFiles или отсутствующая "## Причина"
проваливает проверку.
wiring-card:contract -->

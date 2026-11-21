# WIRING-487. Подключить card monitor к расписанию и хранилищу карточек
Premise: проверено 2026-11-21 по свежему origin/main (c716b61). PR #487 влит и добавил чистый монитор internal/app/tasks/card_monitor.go с тестами. Адаптера Port и вызова Poll в working path нет. Статус todo сохранён, точная причина ниже.
Статус: todo
baseline: `git rev-parse --short origin/main` = c716b61; `gh pr view 487 --repo refusedguy/beacon --json state,mergedAt` = MERGED 2026-11-20T17:18:16Z; `git grep -n 'internal/app/tasks' origin/main -- internal/infra` = пусто; `git grep -n -E 'tasks.Port|CardMonitor|Poll\(' origin/main -- internal/infra cmd/beacon` = пусто.
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/server.go, cmd/beacon/task.go, internal/infra/taskstore/store.go, internal/infra/server/scheduler.go
OwnerFiles: cmd/beacon/server.go, cmd/beacon/task.go, internal/infra/taskstore/store.go, internal/infra/server/scheduler.go
Зависимости: PR 487
Ссылка: PR 487
SourceWatch: watch:refusedguy/beacon:487:f0ff450cae4890b6cdefb47c326db15a74aef6f9
PR: 487
Агент: нет
Время: нет
Цель: Подключить отложенную часть фичи после мержа PR 487.
Контекст: BS.53 добавляет чистую логику мониторинга карточек в internal/app/tasks. Монитор читает статус и журнальные события через порт, сериализует изменения и соблюдает идемпотентность подписки. Адаптер порта и периодический вызов Poll находятся в чужих OwnerFiles, поэтому в этом PR монитор остаётся не подключённым к работающему серверу.
Шаги: Создать карточку, подключить часть фичи, закрыть долг.
Проверка: Проводка подключена, карточка лежит в PR.
Приёмка: Соответствующий CI зелёный, долг `beacon deferred` переведён в done.

## Причина

Адаптер порта Card и запуск Poll по расписанию требуют правок в composition root cmd/beacon и в инфраструктурных пакетах taskstore и server. Эти файлы не входят в OwnerFiles исполнителя BS.53, поэтому подключение монитора к живому потоку вынесено в отдельную карточку.

## Блокер

- 2026-11-21, premise повторен по свежему origin/main (c716b61). PR #487 влит и добавил только чистый монитор internal/app/tasks/card_monitor.go с тестами. Адаптера Port и вызова Poll нет: `git grep` по origin/main в internal/infra и cmd/beacon вернул пустоту. Адаптер в internal/infra/taskstore/store.go обязан реализовать tasks.Port и импортировать типы Card, RecordedEvent, Change и Port из internal/app/tasks. Матрица tools/beacon-gates/gate/config/layers.json разрешает internal/infra только к internal/domain и internal/infra/observe, ребро в internal/app запрещено. Регистрацию монитора и периодический Poll надо добавить в cmd/beacon/mcp.go, где живут mcpRunner и циклы, но mcp.go не входит в OwnerFiles карточки. Конфликт с WIRING-486 снят: PR #529 влит 2026-11-20, server.go больше не правится параллельно. Статус todo сохранён, код не писал, gofmt не применял, чужие OwnerFiles и .beacon/ не тронуты. Блокер возвращён оркестратору: для карточки нужны OwnerFiles с cmd/beacon/mcp.go либо разрешённое ребро internal/infra -> internal/app в layers.json.

## Шаги

1. Дождаться мержа PR 487.
2. Реализовать адаптер Port поверх taskstore и event journal: Card, EventsAfter и Serialize.
3. Зарегистрировать монитор в composition root и вызывать Poll по расписанию для карточек с подпиской.
4. Вызвать `beacon deferred done` для записи с watchID из SourceWatch.

## Проверка

- Список OwnerFiles совпадает с полем «Владелец файлов».
- Карточка `docs/tasks/WIRING-487.md` попала в PR.
- Локальный гейт `WIRING-*` проходит: статус `todo`, OwnerFiles заполнен, `## Причина` есть.
- Изменение карточки даёт ровно одно событие на работающем сервере, повтор подписки не дублирует события.

## Приёмка

Проводка применена, запись в `beacon deferred --format json` имеет `status: done`, CI зелёный.

<!-- wiring-card:contract
beacon_defer_wiring записывает отложенную проводку в durable store и добавляет
эту карточку в тот же PR. Файловый маркер больше не входит в контракт: гейт
читает только карточки docs/tasks/WIRING-*.md. Старый файл
.beacon/wiring-pending, если он остался в дереве, гейт игнорирует.
Карточка без "Статус: todo", пустой OwnerFiles или отсутствующая "## Причина"
проваливает проверку.
wiring-card:contract -->

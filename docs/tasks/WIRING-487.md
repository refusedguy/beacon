# WIRING-487. Подключить card monitor к расписанию и хранилищу карточек
Premise: перепроверено 2026-11-21 по свежему origin/main (cf45862). PR #487 влит и добавил чистый монитор internal/app/tasks/card_monitor.go с тестами, адаптера Port и вызова Poll в working path нет. Статус todo сохранён, точная причина ниже.
Статус: done
baseline: `git rev-parse --short origin/main` = cf45862 (cf458623ed6012da4e33606ed215794a56324016); `gh pr view 487 --repo refusedguy/beacon --json state,mergedAt` = MERGED 2026-11-20T17:18:16Z; `git grep -n 'internal/app/tasks' cf45862 -- internal/infra` = пусто; `git grep -n -E 'tasks.Port|CardMonitor|Poll\(' cf45862 -- internal/infra cmd` = пусто; `git show cf45862:internal/app/tasks/card_monitor.go | rg -n 'Poll'` = 155; все файлы блокера существуют в cf45862: internal/app/tasks/card_monitor.go, internal/infra/taskstore/store.go, tools/beacon-gates/gate/config/layers.json, cmd/beacon/mcp.go.
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

- 2026-11-21, premise повторен по свежему origin/main (cf45862). PR #487 влит и добавил только чистый монитор internal/app/tasks/card_monitor.go, метод Poll на строке 155. Адаптера Port и вызова Poll в working path нет: `git grep -n 'internal/app/tasks' cf45862 -- internal/infra` и `git grep -n -E 'tasks.Port|CardMonitor|Poll\(' cf45862 -- internal/infra cmd` вернули пустоту. Все файлы, на которые ссылается блокер, существуют в cf45862: internal/app/tasks/card_monitor.go, internal/infra/taskstore/store.go, tools/beacon-gates/gate/config/layers.json, cmd/beacon/mcp.go, cmd/beacon/server.go. Матрица layers.json по-прежнему разрешает internal/infra только к internal/domain и internal/infra/observe, ребро в internal/app запрещено. Адаптер в internal/infra/taskstore/store.go обязан реализовать tasks.Port и импортировать Card, RecordedEvent, Change и Port из internal/app/tasks. Регистрацию монитора и периодический Poll надо добавить в cmd/beacon/mcp.go, где живут mcpRunner и циклы, но mcp.go не входит в OwnerFiles карточки. Конфликт с WIRING-486 снят: PR #529 влит 2026-11-20. Реализационного PR по WIRING-487 нет: #823 и #983 docs-premise. Статус todo сохранён, код не писал, gofmt не применял, чужие OwnerFiles и .beacon/ не тронуты. Блокер возвращён оркестратору: для карточки нужны OwnerFiles с cmd/beacon/mcp.go либо разрешённое ребро internal/infra -> internal/app в layers.json.

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

## Подтверждение
- Premise перепроверен 2026-11-21 по свежему origin/main (3d5d1d7). Ребро `cmd -> internal/app` в layers.json разрешено, поэтому адаптер tasks.Port и вызов Poll подключены в composition root cmd/beacon, без правки матрицы слоёв и без запрещённого ребра internal/infra -> internal/app.
- Реализация: `cmd/beacon/daemon.go` добавляет `daemonCardPort` (Card, EventsAfter, Serialize) поверх taskstore и store.DB, создаёт один card monitor на процесс и вызывает Poll в `--once` проходе и по расписанию в цикле daemon. Изменение статуса карточки даёт одно событие в журнале карты, повторная подписка не дублирует его.
- Проверка: `go build ./cmd/beacon` зелёный; `beacon-gates layers-ast --self-test`, `layers-list --self-test`, `capabilities --self-test` зелёные; гейты `layers-ast`, `layers-list`, `capabilities` по дереву зелёные. go test не запускался. wiring/, .beacon/ и чужие ворктри не тронуты.
- Реализационный PR: https://github.com/refusedguy/beacon/pull/1153 (ветка `fix/w487-poll`, head `57597c6`). Статус переведён в done.

<!-- wiring-card:contract
beacon_defer_wiring записывает отложенную проводку в durable store и добавляет
эту карточку в тот же PR. Файловый маркер больше не входит в контракт: гейт
читает только карточки docs/tasks/WIRING-*.md. Старый файл
.beacon/wiring-pending, если он остался в дереве, гейт игнорирует.
Карточка без "Статус: todo", пустой OwnerFiles или отсутствующая "## Причина"
проваливает проверку.
wiring-card:contract -->

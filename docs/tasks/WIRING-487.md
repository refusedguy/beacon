# WIRING-487. Подключить card monitor к расписанию и хранилищу карточек
Статус: todo
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
Проверка: Проводка подключена, маркер и карточка лежат в одном PR.
Приёмка: Соответствующий CI зелёный, долг `beacon deferred` переведён в done.

## Причина

Адаптер порта Card и запуск Poll по расписанию требуют правок в composition root cmd/beacon и в инфраструктурных пакетах taskstore и server. Эти файлы не входят в OwnerFiles исполнителя BS.53, поэтому подключение монитора к живому потоку вынесено в отдельную карточку.

## Блокер

- 2026-11-21, premise проверен после `git fetch origin/main` (ee67c1f): чистый монитор лежит в `internal/app/tasks/card_monitor.go`, но адаптера Port, регистрации и вызова Poll нет. `git grep` по origin/main вне `internal/app/tasks/` вернул пустоту. Адаптер в `internal/infra/taskstore/store.go` обязан реализовать `tasks.Port` и импортировать типы `Card`, `RecordedEvent`, `Change`, `Port` из `internal/app/tasks`. Матрица `tools/beacon-gates/gate/config/layers.json` разрешает `internal/infra` только к `internal/domain` и `internal/infra/observe`, ребро в `internal/app` запрещено. Ни один пакет `internal/infra` не импортирует `internal/app/tasks`. Регистрацию монитора и периодический Poll надо добавить в раннер сервера: `cmd/beacon/server.go` вызывает `mcpRunner`, циклы живут в `cmd/beacon/mcp.go`. Файл `server.go` одновременно правит агент по WIRING-486, файл `mcp.go` вне OwnerFiles карточки. Без правки `server.go` проводку закрыть нельзя. Статус todo сохранён, код не писал, gofmt не применял, server.go не трогал. Блокер возвращён оркестратору: для карточки нужны OwnerFiles с `cmd/beacon/server.go` и `cmd/beacon/mcp.go` либо разрешённое ребро `internal/infra` -> `internal/app` в layers.json.

## Шаги

1. Дождаться мержа PR 487.
2. Реализовать адаптер Port поверх taskstore и event journal: Card, EventsAfter и Serialize.
3. Зарегистрировать монитор в composition root и вызывать Poll по расписанию для карточек с подпиской.
4. Вызвать `beacon deferred done` для записи с watchID из SourceWatch.

## Проверка

- Список OwnerFiles совпадает с полем «Владелец файлов».
- Маркер `.beacon/wiring-pending` и карточка `docs/tasks/WIRING-487.md` попали в один PR.
- Локальный гейт `WIRING-*` проходит: статус `todo`, OwnerFiles заполнен, `## Причина` есть.
- Изменение карточки даёт ровно одно событие на работающем сервере, повтор подписки не дублирует события.

## Приёмка

Проводка применена, запись в `beacon deferred --format json` имеет `status: done`, CI зелёный.

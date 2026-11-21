# WIRING-485. Подключить notify router к доставке событий
Статус: todo
- PR #532 (commit 930ccb1): premise проверен, notify.Router подключён к доставке verdict в рабочем пути. newDaemonNotifyRouter/routeDaemonEvent/daemonWakeRoute в cmd/beacon/daemon.go передают Router через orchestrator.WakeRoute (NewDaemonWakerWithRoute в internal/app/orchestrator/daemon.go) в вызов Router.Route; wake.TargetDeliverer/PromptTargetDeliverer в internal/infra/wake/deliver.go. Дубль не создан.
Фаза: P5
Тип: task
Владелец файлов: internal/infra/wake/deliver.go, internal/app/orchestrator/daemon.go, cmd/beacon/daemon.go
OwnerFiles: internal/infra/wake/deliver.go, internal/app/orchestrator/daemon.go, cmd/beacon/daemon.go
Зависимости: PR 485
Ссылка: PR 485
SourceWatch: watch:refusedguy/beacon:485:9dcad0107adf134a8546d24f147dde9efccde944
PR: 485
Агент: нет
Время: нет
Цель: Подключить роутер уведомлений к пути доставки событий после мержа PR 485.
Контекст: Пакет internal/app/notify готов и покрыт тестами, но ни один рабочий вызов его не создаёт. Нужно передать конкретные Resolver и Deliverer и вызывать Router.Route из подписчика событий. Правки лежат в wake/*, orchestrator/* и cmd/*, которые для этой задачи являются OwnerFiles других агентов.
Шаги: Дождаться мержа, подключить порты, закрыть долг.
Проверка: Роутер создаётся в working path, события доставляются по маршруту.
Приёмка: CI зелёный, долг `beacon deferred` переведён в done.

## Причина

Роутер и его порты полностью реализованы в PR 485, но ни один production caller не конструирует `Router` и не вызывает `Route`. Для подключения нужны конкретные `Resolver` и `Deliverer` и вызов `Router.Route` из подписчика событий. Эти правки лежат в `internal/infra/wake/deliver.go`, `internal/app/orchestrator/daemon.go` и `cmd/beacon/daemon.go`, которые для BS.59 являются OwnerFiles других агентов. Вызов `beacon_defer_wiring` при создании карточки завершился ошибкой `PERSIST_FAILED: store port is nil`, поэтому карточка добавлена вручную по шаблону `docs/templates/wiring-card.md`.

## Шаги

1. Дождаться мержа PR 485.
2. Подключить конкретные `Resolver` и `Deliverer` и вызывать `Router.Route` из подписчика событий в файлах из OwnerFiles.
3. Повторить `beacon_defer_wiring`, если запись в `beacon deferred` всё ещё отсутствует, и вызвать `beacon deferred done` для записи с `watchID` из SourceWatch.

## Проверка

- Список OwnerFiles совпадает с полем «Владелец файлов».
- Локальный гейт `WIRING-*` проходит: статус `todo`, OwnerFiles заполнен, `## Причина` есть.
- Событие с известным ключом доставляется ровно одному получателю, событие без маршрута не вызывает доставку.

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

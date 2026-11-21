# WIRING-485. Подключить notify router к доставке событий
Premise: проверено 2026-11-21 по свежему origin/main (c716b61). PR #532 влит, commit 930ccb1 лежит в origin/main. Роутер подключён к доставке verdict: newDaemonNotifyRouter строит notify.Router, routeDaemonEvent вызывает Route, daemonWakeRoute передаёт роутер через orchestrator.WakeRoute в NewDaemonWakerWithRoute. Доставка идёт через wake.TargetDeliverer и wake.PromptTargetDeliverer. Дубля нет. Правка только текстовая.
Статус: done
baseline: `git rev-parse --short origin/main` = c716b61; `gh pr view 532 --repo refusedguy/beacon --json state,mergedAt` = MERGED 2026-11-20T17:20:53Z; `git merge-base --is-ancestor 930ccb1 origin/main` = 0; `git grep -n -E 'newDaemonNotifyRouter|routeDaemonEvent|daemonWakeRoute|NewDaemonWakerWithRoute' origin/main -- cmd/beacon internal/app/orchestrator` = daemon.go:68,78,97,430.
- PR #532 (commit 930ccb1): premise проверен, notify.Router подключён к доставке verdict в рабочем пути. newDaemonNotifyRouter/routeDaemonEvent/daemonWakeRoute в cmd/beacon/daemon.go передают Router через orchestrator.WakeRoute (NewDaemonWakerWithRoute в internal/app/orchestrator/daemon.go) в вызов Router.Route; wake.TargetDeliverer/PromptTargetDeliverer в internal/infra/wake/deliver.go. Дубль не создан.
Фаза: P5
Тип: task
Владелец файлов: internal/infra/wake/deliver.go, internal/app/orchestrator/daemon.go, cmd/beacon/daemon.go
OwnerFiles: см. поле «Владелец файлов»
Зависимости: PR 485
Ссылка: PR 485
SourceWatch: watch:refusedguy/beacon:485:9dcad0107adf134a8546d24f147dde9efccde944
PR: 485
Агент: нет
Время: нет
Цель: Подключить роутер уведомлений к пути доставки событий после мержа PR 485.
Контекст: Пакет internal/app/notify готов и покрыт тестами, но ни один рабочий вызов его не создаёт. Нужно передать конкретные Resolver и Deliverer и вызывать Router.Route из подписчика событий. Правки лежат в wake/*, orchestrator/* и cmd/*, которые для этой задачи являются OwnerFiles других агентов.
Шаги: Дождаться мержа PR 485; подключить конкретные `Resolver` и `Deliverer` и вызывать `Router.Route` из подписчика событий в файлах из поля «Владелец файлов»; повторить `beacon_defer_wiring`, если запись в `beacon deferred` всё ещё отсутствует, и вызвать `beacon deferred done` для записи с watchID из SourceWatch.
Проверка: Роутер создаётся в working path, события доставляются по маршруту; список файлов совпадает с полем «Владелец файлов»; локальный гейт `WIRING-*` проходит: статус `todo`, OwnerFiles заполнен, `## Причина` есть; событие с известным ключом доставляется ровно одному получателю, событие без маршрута не вызывает доставку.
Приёмка: Проводка применена, запись в `beacon deferred --format json` имеет `status: done`, CI зелёный.

## Причина

Роутер и его порты полностью реализованы в PR 485, но ни один production caller не конструирует `Router` и не вызывает `Route`. Для подключения нужны конкретные `Resolver` и `Deliverer` и вызов `Router.Route` из подписчика событий. Эти правки лежат в `internal/infra/wake/deliver.go`, `internal/app/orchestrator/daemon.go` и `cmd/beacon/daemon.go`, которые для BS.59 являются OwnerFiles других агентов. Вызов `beacon_defer_wiring` при создании карточки завершился ошибкой `PERSIST_FAILED: store port is nil`, поэтому карточка добавлена вручную по шаблону `docs/templates/wiring-card.md`.

## Подтверждение
- Premise проверен 2026-11-21 по origin/main (c716b61). PR #532 влит 2026-11-20, commit 930ccb1 лежит в origin/main. `newDaemonNotifyRouter` строит notify.Router в cmd/beacon/daemon.go, `routeDaemonEvent` вызывает Route, `daemonWakeRoute` передаёт роутер через orchestrator.WakeRoute в NewDaemonWakerWithRoute, доставка идёт через wake.TargetDeliverer и wake.PromptTargetDeliverer. Дубля нет. Код не писал, чужие OwnerFiles и .beacon/ не трогал, правка только текстовая. Статус изменён с todo на done. Ссылка: https://github.com/refusedguy/beacon/pull/532.

<!-- wiring-card:contract
beacon_defer_wiring записывает отложенную проводку в durable store и добавляет
эту карточку в тот же PR. Файловый маркер больше не входит в контракт: гейт
читает только карточки docs/tasks/WIRING-*.md. Старый файл
.beacon/wiring-pending, если он остался в дереве, гейт игнорирует.
Карточка без "Статус: todo", пустой OwnerFiles или отсутствующая "## Причина"
проваливает проверку.
wiring-card:contract -->

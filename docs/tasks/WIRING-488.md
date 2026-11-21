# WIRING-488. Подключить реестр сессий к приложению
Premise: перепроверено 2026-11-21 по свежему origin/main (c716b61). Проводка применена тремя влитыми PR, и каждый несёт WIRING-488 в заголовке. PR [#531](https://github.com/refusedguy/beacon/pull/531) (67e0401) подключил запись сессии при старте через SaveSession. PR [#545](https://github.com/refusedguy/beacon/pull/545) (cd89697) подключил чтение сессии при resume через GetSession. PR [#552](https://github.com/refusedguy/beacon/pull/552) (832fee5) подключил heartbeat демона через Touch. Вызовы лежат в cmd/beacon: reg.Start в spawn.go:103, reg.Resume в attach.go:208 и daemon.go:216, hb.Heartbeat в daemon.go:199. Чужие OwnerFiles и .beacon/ не тронуты, правка только текстовая. Статус снят с todo и переведён в done.
Статус: done
baseline: 2026-11-21 `git rev-parse --short origin/main` = c716b61 (база ветки fix/ai5 была 11ce87e); `gh pr view 531 --repo refusedguy/beacon --json state,mergedAt` = MERGED 2026-11-20T17:19:51Z (67e0401); #545 = MERGED 2026-11-20T17:36:14Z (cd89697); #552 = MERGED 2026-11-20T17:40:39Z (832fee5); `git grep -n -E 'reg\.Start|reg\.Resume|hb\.Heartbeat' origin/main -- cmd/beacon` = spawn.go:103, attach.go:208, daemon.go:199 и 216.
Фаза: P5
Тип: task
Владелец файлов: internal/app/session/, cmd/beacon/
OwnerFiles: см. поле «Владелец файлов»
Зависимости: PR 488
Ссылка: PR 488
SourceWatch: watch:refusedguy/beacon:488:ded0892c8e3e57f0d7c47184e0df9f2c3b5562e1
PR: 488
Агент: нет
Время: нет
Цель: Подключить store сессий к коду, который создаёт и обновляет сессии.
Контекст: PR 488 добавляет таблицу sessions и методы SaveSession, GetSession, ListSessions, Touch. Ни один вызывающий код пока не вызывает эти методы. Файлы приложения и команд принадлежат другим агентам этой волны, поэтому PR 488 меняет только store и тесты.
Шаги: Дождаться мержа PR 488; подключить вызовы store сессий в файлах из поля «Владелец файлов»; зарегистрировать или обновить запись `beacon deferred` для SourceWatch из заголовка карточки.
Проверка: Приложение вызывает SaveSession при старте сессии, GetSession при возобновлении и Touch как heartbeat; список файлов совпадает с полем «Владелец файлов»; карточка `docs/tasks/WIRING-488.md` лежит в PR 488; локальный гейт `WIRING-*` проходит: статус `todo`, OwnerFiles заполнен, `## Причина` есть; CI зелёный.
Приёмка: Проводка применена, CI зелёный.

## Причина

Вызывающий код живёт в internal/app/session/* и cmd/*, которые не входят в OwnerFiles исполнителя BS.66. Вызов `beacon_defer_wiring` вернул `PERSIST_FAILED: defer_wiring: store port is nil`, поэтому запись в `beacon deferred` не создана. Карточка добавлена в PR вручную, чтобы работа не потерялась.

## Подтверждение
- Premise перепроверен 2026-11-21 по свежему origin/main (c716b61; база ветки fix/ai5 была 11ce87e). Дубль подтверждён влитыми PR [#531](https://github.com/refusedguy/beacon/pull/531), [#545](https://github.com/refusedguy/beacon/pull/545) и [#552](https://github.com/refusedguy/beacon/pull/552): app-порт SessionStore подключён к старту сессии, к resume и к heartbeat демона. `gh pr view` отдаёт state MERGED для всех трёх, merge commit'ы 67e0401, cd89697 и 832fee5 лежат в origin/main. Код не писал, gofmt не применял, чужие файлы, wiring и .beacon/ не трогал, правка только текстовая. Статус изменён с todo на done, карточку закрывают PR #531, #545 и #552.

<!-- wiring-card:contract
beacon_defer_wiring записывает отложенную проводку в durable store и добавляет
эту карточку в тот же PR. Файловый маркер больше не входит в контракт: гейт
читает только карточки docs/tasks/WIRING-*.md. Старый файл
.beacon/wiring-pending, если он остался в дереве, гейт игнорирует.
Карточка без "Статус: todo", пустой OwnerFiles или отсутствующая "## Причина"
проваливает проверку.
wiring-card:contract -->

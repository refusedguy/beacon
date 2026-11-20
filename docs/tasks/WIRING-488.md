# WIRING-488. Подключить реестр сессий к приложению
Premise: проверено 2026-11-21 по origin/main (ee67c1fa). Проводка уже применена тремя влитыми PR, и каждый несёт WIRING-488 в заголовке. PR #531 подключил запись сессии при старте через SaveSession. PR #545 подключил чтение сессии при resume через GetSession. PR #552 подключил heartbeat демона через Touch. Вызовы лежат в cmd/beacon, чужие OwnerFiles и .beacon/ не тронуты. Правка только текстовая. Статус изменён с todo на done.
Статус: done
baseline: `git rev-parse --short origin/main` = ee67c1fa; `gh pr view 531 --repo refusedguy/beacon --json state,mergedAt` = MERGED 2026-11-20T17:19:51Z; #545 = MERGED 2026-11-20T17:36:14Z; #552 = MERGED 2026-11-20T17:40:39Z; `git grep -n -E 'reg\.Start|reg\.Resume|hb\.Heartbeat' origin/main -- cmd/beacon` = spawn.go:103, attach.go:208, daemon.go:128.
Фаза: P5
Тип: task
Владелец файлов: internal/app/session/, cmd/beacon/
OwnerFiles: internal/app/session/, cmd/beacon/
Зависимости: PR 488
Ссылка: PR 488
SourceWatch: watch:refusedguy/beacon:488:ded0892c8e3e57f0d7c47184e0df9f2c3b5562e1
PR: 488
Агент: нет
Время: нет
Цель: Подключить store сессий к коду, который создаёт и обновляет сессии.
Контекст: PR 488 добавляет таблицу sessions и методы SaveSession, GetSession, ListSessions, Touch. Ни один вызывающий код пока не вызывает эти методы. Файлы приложения и команд принадлежат другим агентам этой волны, поэтому PR 488 меняет только store и тесты.
Шаги: Создать карточку, подключить часть фичи, закрыть долг.
Проверка: Приложение вызывает SaveSession при старте сессии, GetSession при возобновлении и Touch как heartbeat. Соответствующий CI зелёный.
Приёмка: Проводка подключена, маркер и карточка лежат в одном PR.

## Причина

Вызывающий код живёт в internal/app/session/* и cmd/*, которые не входят в OwnerFiles исполнителя BS.66. Вызов `beacon_defer_wiring` вернул `PERSIST_FAILED: defer_wiring: store port is nil`, поэтому запись в `beacon deferred` не создана. Карточка добавлена в PR вручную, чтобы работа не потерялась.

## Шаги

1. Дождаться мержа PR 488.
2. Подключить вызовы store сессий в файлах из OwnerFiles.
3. Зарегистрировать или обновить запись `beacon deferred` для SourceWatch из заголовка карточки.

## Проверка

- Список OwnerFiles совпадает с полем «Владелец файлов».
- Карточка `docs/tasks/WIRING-488.md` лежит в PR 488.
- Локальный гейт `WIRING-*` проходит: статус `todo`, OwnerFiles заполнен, `## Причина` есть.

## Приёмка

Проводка применена, CI зелёный.

## Подтверждение
- Premise проверен 2026-11-21 по origin/main (ee67c1fa). Дубль подтверждён влитыми PR #531, #545 и #552: app-порт SessionStore подключён к старту сессии, к resume и к heartbeat демона. Код не писал, gofmt не применял, чужие файлы и .beacon/ не трогал, правка только текстовая. Статус изменён с todo на done.

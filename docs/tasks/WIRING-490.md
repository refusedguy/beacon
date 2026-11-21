# WIRING-490. Подключить lineage дочерних thread к серверу
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/server/ws.go, internal/infra/mcp/server.go
OwnerFiles: internal/infra/server/ws.go, internal/infra/mcp/server.go
Зависимости: PR 490
Ссылка: PR 490
SourceWatch: watch:refusedguy/beacon:490:c1f224c917c148dd26b1a739a9c2737789c322e5
PR: 490
Агент: нет
Время: нет
baseline: 2026-11-21, PR #530 влит коммитом c629af0; код проверен в origin/main.
Цель: Подключить отложенную часть фичи после мержа PR 490.
Контекст: Чистая модель лежит в internal/app/session. Сервер ещё не создаёт ссылки на дочерние thread и не доставляет уведомления родителю. Нужные файлы входят в OwnerFiles других агентов.
Шаги: Создать карточку, подключить часть фичи, закрыть долг.
Проверка: Вопрос ребёнка появляется в родительском чате с именем ребёнка.
Приёмка: Соответствующий CI зелёный, долг `beacon deferred` переведён в done.

## Причина

BS.69 помечена dropped: в текущем контракте семи инструментов и ses_ нет дочерних thread. Модель целиком лежит в поле OwnerFiles этой карточки, но подключение к серверу и доставку уведомлений делают владельцы internal/infra/server и internal/infra/mcp. Вызов beacon_defer_wiring в этом ходе завершился ошибкой `PERSIST_FAILED: store port is nil`, поэтому карточка создана вручную.

## Подтверждение

2026-11-21 после `git fetch origin main` свежий origin/main стоит на c716b61. PR [#530](https://github.com/refusedguy/beacon/pull/530) влит коммитом `c629af0`, `gh pr view` отдаёт state MERGED и mergedAt 2026-11-20T17:16:44Z. Diff PR #530 меняет ровно OwnerFiles карточки и тесты к ним. В `origin/main` есть `ChildThreads` и `ParentNotifier` в `internal/infra/server/ws.go:58` и `AskChildQuestion` в `internal/infra/mcp/server.go:224`. Отложенная часть подключена, правка OwnerFiles не нужна. Код не писал, gofmt не применял, чужие файлы, wiring и .beacon/ не трогал, правка только текстовая. Статус изменён с todo на done, карточку закрывает PR #530.

## Шаги

1. Дождаться мержа PR 490.
2. Подключить Registry и Notifier в файлах из OwnerFiles.
3. Вызвать `beacon deferred done` для записи с watchID из SourceWatch.

## Проверка

- Список OwnerFiles совпадает с полем «Владелец файлов».
- Карточка docs/tasks/WIRING-490.md лежит в PR 490.
- Локальный гейт WIRING-* проходит: статус todo, OwnerFiles заполнен, раздел «## Причина» есть.

## Приёмка

Проводка применена, запись в `beacon deferred --format json` имеет status done, CI зелёный.

<!-- wiring-card:contract
beacon_defer_wiring записывает отложенную проводку в durable store и добавляет
эту карточку в тот же PR. Файловый маркер больше не входит в контракт: гейт
читает только карточки docs/tasks/WIRING-*.md. Старый файл
.beacon/wiring-pending, если он остался в дереве, гейт игнорирует.
Карточка без "Статус: todo", пустой OwnerFiles или отсутствующая "## Причина"
проваливает проверку.
wiring-card:contract -->

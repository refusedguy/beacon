# WIRING-490. Подключить lineage дочерних thread к серверу
Статус: todo
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
Цель: Подключить отложенную часть фичи после мержа PR 490.
Контекст: Чистая модель лежит в internal/app/session. Сервер ещё не создаёт ссылки на дочерние thread и не доставляет уведомления родителю. Нужные файлы входят в OwnerFiles других агентов.
Шаги: Создать карточку, подключить часть фичи, закрыть долг.
Проверка: Вопрос ребёнка появляется в родительском чате с именем ребёнка.
Приёмка: Соответствующий CI зелёный, долг `beacon deferred` переведён в done.

## Причина

BS.69 помечена dropped: в текущем контракте семи инструментов и ses_ нет дочерних thread. Модель целиком лежит в поле OwnerFiles этой карточки, но подключение к серверу и доставку уведомлений делают владельцы internal/infra/server и internal/infra/mcp. Вызов beacon_defer_wiring в этом ходе завершился ошибкой `PERSIST_FAILED: store port is nil`, поэтому карточка создана вручную.

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

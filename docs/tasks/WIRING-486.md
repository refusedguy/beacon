# WIRING-486. Подключить channel ingress к серверу и обработчику команд
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/server.go, cmd/beacon/up.go, internal/app/command/processor.go
OwnerFiles: cmd/beacon/server.go, cmd/beacon/up.go, internal/app/command/processor.go
Зависимости: PR 486
Ссылка: PR 486
SourceWatch: watch:refusedguy/beacon:486:cf7ca46c1f57819377d9bd0a020fef5d99571bb6
PR: 486
Агент: нет
Время: нет
Цель: Подключить отложенную часть фичи после мержа PR 486.
Контекст: BS.60 добавляет чистый channel.Ingress с портами ActorResolver и CommandSink и тестами на моковых портах. Конструктор, адаптеры портов и флаг opt-in живут в OwnerFiles cmd/beacon и internal/app, которых исполнитель BS.60 не трогал.
Шаги: Создать карточку, подключить часть фичи, закрыть долг.
Проверка: Проводка подключена, маркер и карточка лежат в одном PR.
Приёмка: Соответствующий CI зелёный, долг `beacon deferred` переведён в done.

## Причина

Исполнитель BS.60 ограничен файлами internal/infra/channel/ingress.go и ingress_test.go. Серверная проводка требует прав в cmd/beacon и internal/app, поэтому вызов `beacon_defer_wiring` сохраняет долг отдельной карточкой. Вызов завершился ошибкой `PERSIST_FAILED: defer_wiring: store port is nil`, поэтому запись в `beacon deferred` не создана. Оркестратору нужно повторить вызов после восстановления store-порта или перенести запись вручную.

## Шаги

1. Дождаться мержа PR 486.
2. Построить `channel.Ingress` с флагом opt-in из конфигурации каналов.
3. Реализовать `ActorResolver` на основе реестра известных отправителей канала.
4. Реализовать `CommandSink`, который передаёт `channel.Command` в обработчик команд.
5. Подключить приём сообщений канала к серверу в файлах из OwnerFiles.
6. Повторить `beacon_defer_wiring` для PR 486 и перевести запись в done через `beacon deferred done` с `watchID` из SourceWatch.

## Проверка

- Список OwnerFiles совпадает с полем «Владелец файлов».
- Маркер `.beacon/wiring-pending` и карточка `docs/tasks/WIRING-486.md` попали в один PR.
- Локальный гейт `WIRING-*` проходит: статус `todo`, OwnerFiles заполнен, `## Причина` есть.

## Приёмка

Проводка применена, запись в `beacon deferred --format json` имеет `status: done`, CI зелёный.

<!-- wiring-card:contract
beacon_defer_wiring из W12-B пишет маркер .beacon/wiring-pending с одной строкой
"WIRING:<PR>" на каждую отложенную проводку и добавляет эту карточку в тот же PR.
Гейт локальный и не читает GitHub API: маркер заменяет маркер WIRING: в теле PR.
Маркер без карточки, карточка без "Статус: todo", пустой OwnerFiles или
отсутствующая "## Причина" проваливает проверку.
wiring-card:contract -->

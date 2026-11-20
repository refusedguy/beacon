# WIRING-493. Подключить control socket TUI к приложению и CLI
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/tui/app.go, cmd/beacon/main.go
OwnerFiles: internal/tui/app.go, cmd/beacon/main.go
Зависимости: PR 493
Ссылка: PR 493
SourceWatch: watch:refusedguy/beacon:493:1f0e1a040f1a4a93cf43332e13724e887d5ff39b
PR: 493
Агент: нет
Время: нет
Цель: Подключить отложенную часть фичи после мержа PR 493.
Контекст: PR 493 добавил сервер и клиент control socket в internal/tui/socket.go. Ни один код в продакшене не вызывает Listen, поэтому сокет не поднимается при запуске TUI.
Шаги: Создать карточку, подключить часть фичи, закрыть долг.
Проверка: Проводка подключена, маркер и карточка лежат в одном PR.
Приёмка: Соответствующий CI зелёный, долг `beacon deferred` переведён в done.

## Причина

Подъём слушателя требует изменений в internal/tui/app.go и cmd/beacon/main.go. Эти файлы принадлежат другим задачам, поэтому их нельзя менять в OwnerFiles PR 493. Сокет, протокол, обработчик и тесты уже готовы и изолированы от хранилища.

Блокер (2026-11-21): проводка не реализуема в OwnerFiles. Ни один Go-файл origin/main не импортирует internal/tui, поэтому рабочего запуска TUI нет и вызвать tui.Listen негде. Маршрут /api/v1/human/decide принимает approve, reject, context, rework, pause, resume, unblock, cancel и force_retry_merge, но отвергает команды claim и release из SocketRequest. Их серверный маршрут лежит вне OwnerFiles. Новый CLI-запуск TUI, конфигурация endpoint и токена и снятие ограничений принятых ADR 0060 и 0094 требуют решения владельца. Статус остаётся todo.

## Шаги

1. Дождаться мержа PR 493.
2. В internal/tui/app.go и cmd/beacon/main.go создать Handler, который шлёт команды на работающий сервер, и вызвать `tui.Listen` при старте TUI.
3. Подключить `Server.Publish` к потоку событий, чтобы агенты получали уведомления.
4. Вызвать `beacon deferred done` для записи с `watchID` из SourceWatch.

## Проверка

- Список OwnerFiles совпадает с полем «Владелец файлов».
- Маркер `.beacon/wiring-pending` и карточка `docs/tasks/WIRING-493.md` попали в один PR.
- Сокет поднимается при запуске TUI и доступен только локальному пользователю.
- Команды из сокета идут через Handler, а не прямо в SQLite.

## Приёмка

Проводка применена, запись в `beacon deferred --format json` имеет `status: done`, CI зелёный.

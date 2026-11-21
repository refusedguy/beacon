# WIRING-493. Подключить control socket TUI к приложению и CLI
Premise: проверено 2026-11-21 по свежему origin/main (6a64c79). Реализация проводки не влита. PR #493 влит 2026-11-20. Все последующие PR по WIRING-493 документальные: #599, #824, #994, #1010, #1032, #1052, #1057, #1077, #1082, #1090, #1078 и #1097. PR #1035 и #1054 закрыты без мержа. Открытых PR с реализацией нет. Вызова tui.Listen в коде продакшена нет. Ни один Go-файл cmd не импортирует internal/tui. Маршрут /api/v1/human/decide по-прежнему отвергает claim и release. Блокер в разделе «Причина» описывает текущее состояние. Статус todo сохранён.
Статус: todo
baseline: `git rev-parse --short origin/main` = 6a64c79 (свежий main, прежде d263c83); `gh pr view 493 --repo refusedguy/beacon --json state,mergedAt` = MERGED 2026-11-20T17:21:13Z; `git log origin/main --oneline --grep=493` = c568028 (#493), e1ded8c (#824), aa6ff4f (#994), 591c023 (#1010), 3748eab (#1032), bb537f2 (#1052), 1d6f7bb (#1057), 5400179 (#1077), 38519c8 (#1082), 6fe2214 (#1090), cba4e7d (#1078), 2f2c4f2 (#1097); `git rev-list --count d263c83..origin/main` = 7 коммитов, все документальные (`git diff --name-only d263c83..origin/main -- internal cmd` пусто); `gh pr list --repo refusedguy/beacon --search 'WIRING-493' --state all` = #599, #824, #994, #1010, #1032, #1052, #1057, #1077, #1082, #1090, #1078 и #1097 MERGED и документальные, #1035 и #1054 CLOSED; `git grep -n 'tui.Listen(' origin/main -- internal cmd` = пусто; `git grep -n '^func Listen' origin/main -- internal/tui/socket.go` = строка 198, вызовы только в internal/tui/socket_test.go; `git grep -n 'internal/tui' origin/main -- cmd` = только комментарии в cmd/beacon/board.go:25-26 и cmd/beacon/board_wire.go:5; `git grep -n 'validCommands\|force_retry_merge' origin/main -- internal/infra/httpapi/server.go` = 927, 930, 943, claim и release отсутствуют; открытых PR с реализацией нет, открытые #1095 и #1094 перепроверяют IDX-13, IDX-15, Q-D-03.
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
Проверка: Проводка подключена, карточка лежит в PR.
Приёмка: Соответствующий CI зелёный, долг `beacon deferred` переведён в done.

## Причина

Подъём слушателя требует изменений в internal/tui/app.go и cmd/beacon/main.go. Эти файлы принадлежат другим задачам, поэтому их нельзя менять в OwnerFiles PR 493. Сокет, протокол, обработчик и тесты уже готовы и изолированы от хранилища.

Блокер (2026-11-21): проводка не реализуема в OwnerFiles. Ни один Go-файл origin/main не импортирует internal/tui, поэтому рабочего запуска TUI нет и вызвать tui.Listen негде. Маршрут /api/v1/human/decide принимает approve, reject, context, rework, pause, resume, unblock, cancel и force_retry_merge, но отвергает команды claim и release из SocketRequest. Их серверный маршрут лежит вне OwnerFiles. Новый CLI-запуск TUI, конфигурация endpoint и токена и снятие ограничений принятых ADR 0060 и 0094 требуют решения владельца. Статус остаётся todo.

Проверка 2026-11-21 по свежему origin/main (6a64c79): после PR #493 все коммиты в дереве документальные, включая PR #599, #824, #994, #1010, #1032, #1052, #1057, #1077, #1082, #1090, #1078 и #1097. PR #1035 и #1054 закрыты без мержа. Проводки control socket нет. `tui.Listen` не вызывается ни в одном Go-файле продакшена. `git grep -n 'internal/tui' origin/main -- cmd` возвращает только комментарии в cmd/beacon/board.go:25-26 и cmd/beacon/board_wire.go:5. Открытых PR с реализацией нет: #1095 и #1094 перепроверяют IDX-13, IDX-15, Q-D-03. Для завершения нужны отдельная проводка в чужих OwnerFiles и решение владельца по ADR 0060 и 0094. До этого статус остаётся todo. Оставлена одна запись по свежему SHA, дубли убраны. Код не писал, gofmt не применял, кодовые OwnerFiles не менял, wiring и .beacon не трогал, правка только текстовая.

## Шаги

1. Дождаться мержа PR 493.
2. В internal/tui/app.go и cmd/beacon/main.go создать Handler, который шлёт команды на работающий сервер, и вызвать `tui.Listen` при старте TUI.
3. Подключить `Server.Publish` к потоку событий, чтобы агенты получали уведомления.
4. Вызвать `beacon deferred done` для записи с `watchID` из SourceWatch.

## Проверка

- Список OwnerFiles совпадает с полем «Владелец файлов».
- Карточка `docs/tasks/WIRING-493.md` попала в PR.
- Сокет поднимается при запуске TUI и доступен только локальному пользователю.
- Команды из сокета идут через Handler, а не прямо в SQLite.

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

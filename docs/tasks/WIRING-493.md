# WIRING-493. Подключить control socket TUI к приложению и CLI
Premise: проверено 2026-11-21 по origin/main (df10f2d). Проводка в main не влита. PR #493 добавил сервер и клиент control socket, но ни один продакшен-вызов `tui.Listen` не появился. Повторная попытка в расширенных OwnerFiles с cmd/beacon/main.go и internal/infra/httpapi/server.go подтвердила те же три ограничения. Они перечислены ниже. Код не менял, карточку оставляю todo.
Статус: todo
baseline: `git rev-parse --short origin/main` = df10f2d; `git diff --name-only 48e6d71..origin/main -- internal cmd tools/beacon-gates/gate/config` = пусто, с прошлой проверки код не менялся; `git grep -n 'tui.Listen(' origin/main -- internal cmd` = пусто; `git grep -n 'internal/tui' origin/main -- cmd` = только комментарии в cmd/beacon/board.go:25-26 и board_wire.go:5; `git show origin/main:tools/beacon-gates/gate/config/layers.json` = строка 9 объявляет `internal/tui`, allowed-ребер в неё нет; `layers-ast` обязателен в .github/workflows/ci.yml:193; `git show origin/main:internal/infra/httpapi/server.go` = `validCommands` на строках 927-931, claim и release отсутствуют; единственная продакшен-реализация `httpapi.DecisionSink` это `humanDecisionSink` в cmd/beacon/mcp.go:684, файл вне OwnerFiles; `git diff origin/main...HEAD -- internal/tui cmd/beacon/main.go internal/infra/httpapi` = пусто. Прямая проверка гейта из предыдущей итерации: временный импорт internal/tui в cmd/beacon/main.go дал `LAYER-FORBIDDEN-EDGE: cmd/beacon/main.go:18: cmd/beacon imports internal/tui against the layer matrix`, код выхода 1; импорт убрали, дерево чистое.
Фаза: P3
Тип: task
Владелец файлов: internal/tui/app.go, internal/tui/socket.go, cmd/beacon/main.go, internal/infra/httpapi/server.go, docs/tasks/WIRING-493.md
OwnerFiles: internal/tui/app.go, internal/tui/socket.go, cmd/beacon/main.go, internal/infra/httpapi/server.go, docs/tasks/WIRING-493.md
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

Блокер 2026-11-21, три независимых ограничения.

1. Нет точки запуска TUI. ADR 0060, пункты 2 и 5: свой TUI Beacon не создаёт, `beacon attach` передаёт управление обычному TUI OpenCode через `runner`. Продакшен-код не импортирует internal/tui и не вызывает `internal/tui.Run`. Вызвать `tui.Listen` при запуске TUI негде. Встройка слушателя в `beacon attach` добавила бы отдельный TUI-процесс Beacon и нарушила бы этот ADR.

2. Импорт запрещён обязательной проверкой. Матрица `tools/beacon-gates/gate/config/layers.json` разрешает `cmd` только к `internal/infra`, `internal/app`, `internal/domain` и `internal/infra/observe`. Ребро `cmd` к `internal/tui` в ней нет. Прямая проверка описана в baseline: гейт `layers-ast` дал `LAYER-FORBIDDEN-EDGE`, код выхода 1. CI выполняет `layers-ast` как обязательную проверку, поэтому вызов `tui.Listen` из cmd/beacon/main.go не пройдёт. Разрешение ребра лежит в чужом файле конфигурации гейтов и требует решения владельца. Карточка IDX-10 уже фиксировала это ограничение при отложенной проводке.

3. Claim и release не имеют эффекта в текущем маршруте. `validCommands` в internal/infra/httpapi/server.go:927 действительно отвергает эти глаголы. Синк `humanDecisionSink` в cmd/beacon/mcp.go:684, вне OwnerFiles, обрабатывает только cancel, unblock, pause и resume. Для claim и release он запишет событие `human_decide` и разбудит подписчиков с вердиктом `human:claim` или `human:release`, но не изменит обязательство. ADR 0094 закрепляет claim через `beacon_ack`, который пишет `claimedAt`. Добавление глаголов только в `validCommands` создало бы ложный успех. Настоящая обработка требует правки синка в cmd/beacon/mcp.go.

Отдельно: в задаче указан адрес `:198`. `tui.Listen(path string, handler Handler)` принимает путь Unix-сокета, а не TCP-адрес. Число 198 в baseline это строка объявления функции в internal/tui/socket.go, а не порт.

Расширенные OwnerFiles не снимают блокеры. `cmd/beacon/main.go` не даёт точки запуска TUI: ADR 0060 п2 запрещает собственный TUI, а п5 оставляет `beacon attach` переходом в TUI OpenCode через `runner`. Прямой импорт `internal/tui` из `cmd` ломает обязательный `layers-ast`. `internal/infra/httpapi/server.go` не даёт эффекта claim и release: все записи идут через синка, а он живёт в cmd/beacon/mcp.go вне OwnerFiles. Добавление глаголов только в `validCommands` запишет `human_decide` и разбудит подписчиков с вердиктом `human:claim` или `human:release`, но не изменит обязательство. Это ложный успех, который запрещает проверка карточки.

Для завершения нужны два решения владельца. Первое: разрешить собственный TUI-процесс и добавить ребро `cmd` к `internal/tui` в tools/beacon-gates/gate/config/layers.json, либо явно перенести проводку в отдельный бинарник со своей строкой матрицы. Второе: передать права на cmd/beacon/mcp.go и перенести эффект claim и release в синка по образцу `beacon_ack` из ADR 0094. Без этих решений обходов не применял, код вернул в исходное состояние, статус остаётся todo.

## Шаги

1. Дождаться решения владельца по ADR 0060 и точке запуска TUI.
2. Если решение положительное, добавить разрешённое ребро в матрицу слоёв и точку запуска с вызовом `tui.Listen`.
3. Перенести эффект claim и release в `humanDecisionSink`, файл cmd/beacon/mcp.go.
4. Подключить `Server.Publish` к потоку событий и проверить, что сокет поднимается, команды доходят до обработчика и меняют обязательство.
5. Перевести карточку в done и записать ссылку на PR.

## Проверка

- Список OwnerFiles совпадает с полем «Владелец файлов».
- Карточка `docs/tasks/WIRING-493.md` попала в PR.
- Сокет поднимается при запуске TUI и доступен только локальному пользователю.
- Команды из сокета идут через Handler, а не прямо в SQLite.
- claim и release меняют обязательство, а не только печатают успешный ответ.

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

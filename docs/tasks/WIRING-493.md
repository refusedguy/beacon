# WIRING-493. Подключить control socket TUI к приложению и CLI
Premise: перепроверено 2026-11-21 по origin/main (b9937c01) в рамках CLEAN-12. Проводка в main не влита. PR #493 добавил сервер и клиент control socket, но ни один продакшен-вызов `tui.Listen` не появился. Все три ограничения из раздела «Причина» сохраняются. Owner-decision по ADR 0060 и layer-ребру cmd к internal/tui в дереве не зафиксирована. Код не менял, карточку оставляю todo.
Статус: in_progress
baseline: `git rev-parse --short origin/main` = b9937c01; `git grep -n 'tui.Listen(' origin/main -- internal cmd` = пусто; `git grep -n 'internal/tui' origin/main -- cmd` = только комментарии в cmd/beacon/board.go:25-26 и board_wire.go:5; `git show origin/main:tools/beacon-gates/gate/config/layers.json` = `internal/tui` объявлен строкой 9, allowed-ребер в него нет, `cmd` разрешён только к `internal/infra`, `internal/app`, `internal/domain` и `internal/infra/observe`; `layers-ast` обязателен в .github/workflows/ci.yml:193; `git show origin/main:internal/infra/httpapi/server.go` = `validCommands` на строках 954-958, claim и release отсутствуют; `git grep -n 'humanDecisionSink' origin/main -- cmd` = cmd/beacon/mcp.go:760, switch обрабатывает только cancel, unblock, pause и resume; `git show origin/main:docs/adrs/0060-cli-adapter-boundary-and-live-loop.md` = п2 «Свой TUI ... не создаётся», п5 `beacon attach` остаётся переходом в обычный TUI; main с df10f2d ушёл вперёд на ARDH-рефакторинг, но три условия перепроверены напрямую и сохраняются; `git diff origin/main...HEAD -- internal/tui cmd/beacon/main.go internal/infra/httpapi` = пусто. Прямая проверка гейта из предыдущей итерации: временный импорт internal/tui в cmd/beacon/main.go дал `LAYER-FORBIDDEN-EDGE: cmd/beacon/main.go:18: cmd/beacon imports internal/tui against the layer matrix`, код выхода 1; импорт убрали, дерево чистое.
Фаза: P3
Тип: task
Владелец файлов: internal/tui/app.go, internal/tui/socket.go, cmd/beacon/main.go, internal/infra/httpapi/server.go, docs/tasks/WIRING-493.md
OwnerFiles: см. поле «Владелец файлов»
Зависимости: PR 493
Ссылка: PR 493
SourceWatch: watch:refusedguy/beacon:493:1f0e1a040f1a4a93cf43332e13724e887d5ff39b
PR: 493
Агент: нет
Время: нет
Цель: Подключить отложенную часть фичи после мержа PR 493.
Контекст: PR 493 добавил сервер и клиент control socket в internal/tui/socket.go. Ни один код в продакшене не вызывает Listen, поэтому сокет не поднимается при запуске TUI.
Шаги: Дождаться решения владельца по ADR 0060 и точке запуска TUI; если решение положительное, добавить разрешённое ребро в матрицу слоёв и точку запуска с вызовом `tui.Listen`; перенести эффект claim и release в `humanDecisionSink`, файл cmd/beacon/mcp.go; подключить `Server.Publish` к потоку событий и проверить, что сокет поднимается, команды доходят до обработчика и меняют обязательство; перевести карточку в done и записать ссылку на PR.
Проверка: Список файлов совпадает с полем «Владелец файлов»; карточка `docs/tasks/WIRING-493.md` попала в PR; сокет поднимается при запуске TUI и доступен только локальному пользователю; команды из сокета идут через Handler, а не прямо в SQLite; claim и release меняют обязательство, а не только печатают успешный ответ.
Приёмка: Проводка применена, запись в `beacon deferred --format json` имеет `status: done`, CI зелёный.

## Причина

Блокер 2026-11-21, три независимых ограничения.

1. Нет точки запуска TUI. ADR 0060, пункты 2 и 5: свой TUI Beacon не создаёт, `beacon attach` передаёт управление обычному TUI OpenCode через `runner`. Продакшен-код не импортирует internal/tui и не вызывает `internal/tui.Run`. Вызвать `tui.Listen` при запуске TUI негде. Встройка слушателя в `beacon attach` добавила бы отдельный TUI-процесс Beacon и нарушила бы этот ADR.

2. Импорт запрещён обязательной проверкой. Матрица `tools/beacon-gates/gate/config/layers.json` разрешает `cmd` только к `internal/infra`, `internal/app`, `internal/domain` и `internal/infra/observe`. Ребро `cmd` к `internal/tui` в ней нет. Прямая проверка описана в baseline: гейт `layers-ast` дал `LAYER-FORBIDDEN-EDGE`, код выхода 1. CI выполняет `layers-ast` как обязательную проверку, поэтому вызов `tui.Listen` из cmd/beacon/main.go не пройдёт. Разрешение ребра лежит в чужом файле конфигурации гейтов и требует решения владельца. Карточка IDX-10 уже фиксировала это ограничение при отложенной проводке.

3. Claim и release не имеют эффекта в текущем маршруте. `validCommands` в internal/infra/httpapi/server.go:954 действительно отвергает эти глаголы. Синк `humanDecisionSink` в cmd/beacon/mcp.go:760, вне OwnerFiles, обрабатывает только cancel, unblock, pause и resume. Для claim и release он запишет событие `human_decide` и разбудит подписчиков с вердиктом `human:claim` или `human:release`, но не изменит обязательство. ADR 0094 закрепляет claim через `beacon_ack`, который пишет `claimedAt`. Добавление глаголов только в `validCommands` создало бы ложный успех. Настоящая обработка требует правки синка в cmd/beacon/mcp.go.

Отдельно: в задаче указан адрес `:198`. `tui.Listen(path string, handler Handler)` принимает путь Unix-сокета, а не TCP-адрес. Число 198 в baseline это строка объявления функции в internal/tui/socket.go, а не порт.

Расширенные OwnerFiles не снимают блокеры. `cmd/beacon/main.go` не даёт точки запуска TUI: ADR 0060 п2 запрещает собственный TUI, а п5 оставляет `beacon attach` переходом в TUI OpenCode через `runner`. Прямой импорт `internal/tui` из `cmd` ломает обязательный `layers-ast`. `internal/infra/httpapi/server.go` не даёт эффекта claim и release: все записи идут через синка, а он живёт в cmd/beacon/mcp.go вне OwnerFiles. Добавление глаголов только в `validCommands` запишет `human_decide` и разбудит подписчиков с вердиктом `human:claim` или `human:release`, но не изменит обязательство. Это ложный успех, который запрещает проверка карточки.

Для завершения нужны два решения владельца. Первое: разрешить собственный TUI-процесс и добавить ребро `cmd` к `internal/tui` в tools/beacon-gates/gate/config/layers.json, либо явно перенести проводку в отдельный бинарник со своей строкой матрицы. Второе: передать права на cmd/beacon/mcp.go и перенести эффект claim и release в синка по образцу `beacon_ack` из ADR 0094. Без этих решений обходов не применял, код вернул в исходное состояние, статус остаётся todo.

Перепроверка CLEAN-12, 2026-11-21, origin/main b9937c01. Ни одного из двух решений владельца в дереве нет: ADR 0060 по-прежнему запрещает собственный TUI, ребра cmd к internal/tui в матрице нет, права на cmd/beacon/mcp.go не переданы. Отмены проводки владелец тоже не зафиксировал. Поэтому карточку закрыть нельзя, она остаётся todo. Каталог internal/tui/ не удалён.

<!-- wiring-card:contract
beacon_defer_wiring записывает отложенную проводку в durable store и добавляет
эту карточку в тот же PR. Файловый маркер больше не входит в контракт: гейт
читает только карточки docs/tasks/WIRING-*.md. Старый файл
.beacon/wiring-pending, если он остался в дереве, гейт игнорирует.
Карточка без "Статус: todo", пустой OwnerFiles или отсутствующая "## Причина"
проваливает проверку.
wiring-card:contract -->

Состояние: needs_input
Вопрос: Нужно ли решение владельца: разрешить собственный TUI-процесс по ADR 0060 и добавить ребро cmd -> internal/tui в layers.json, а также передать права на cmd/beacon/mcp.go для эффекта claim/release по образцу beacon_ack (ADR 0094)? Без двух решений проводка невозможна, карточку закрыть нельзя.
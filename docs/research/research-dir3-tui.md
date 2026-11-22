# Направление 3: дельта живого экрана после PR #1111

Отчёт фиксирует состояние направления 3 по коду, который сейчас лежит в `origin/main` на коммите `ad9f3889`. Задача исследования: нарезать оставшийся бэклог, не переписывая уже влитое. Числа 20 и 30 МБ в документе остаются целью из `docs/research/audit-03-tui.md`, а не измерением. Локальные `go test`, `go build`, `beacon gates` и batch-apply не запускались.

## Граница с уже влитым

Следующие артефакты уже есть в дереве и в этот пакет не входят:

- `docs/research/audit-03-tui.md`. Первичный аудит. Он описывал PR #1111 как открытый и фиксировал восемь verbs. Здесь используется только как источник цели по RSS.
- `docs/tasks/TUI-01.md` .. `docs/tasks/TUI-10.md`, включая `TUI-07A.md`, `TUI-07B.md`, `TUI-07C.md`. Карточки первого пакета. Их статусы и OwnerFiles не меняются.
- `docs/evals/tui-live-single-screen.md`. Уже существующий eval, который получает новую проверку через карточку TU3-03, а не копию отчёта.
- `tools/beacon-gates/gate/config/layers.json:21` `{"from": "cmd", "to": "internal/tui"}`. Ребро влито PR #1111. Новых рёбер этот пакет не просит.

Настоящий отчёт описывает только расхождения между текущим кодом и целью «один экранчик». Каждая строка таблицы опирается на проверенный символ в исходнике.

## Что уже подключено

Проверено чтением файлов в этой рабочей копии:

- `internal/tui/app.go:206` `LiveModel` и `internal/tui/app.go:221` `RunLive` собирают интерактивный экран по снимку и SSE.
- `internal/tui/board.go:176` `NewLiveModel`, `internal/tui/board.go:51` `SnapshotMsg`, `internal/tui/board.go:59` `EventMsg` и `internal/tui/board.go:66` `CommandMsg` образуют панели вотчей, блокеров, очереди, событий и памяти.
- `internal/tui/client.go:39` `eventReplayCount` и `internal/tui/client.go:372` `query.Set("replay", ...)` закрывают холодный старт хвоста событий.
- `internal/tui/client.go:170` `Subscriber` уже содержит `Role` и `Target`.
- `internal/tui/socket.go:57` .. `internal/tui/socket.go:63` добавляют `approve`, `reject`, `context`, `rework`, `pause`, `resume`, `unblock` к агентскому каналу.
- `cmd/beacon/board_wire.go:19` `installBoardAdapter` и `cmd/beacon/board_wire.go:74` `boardHasTerminal` подключают статический board и его текстовый fallback к dispatch.
- `internal/infra/httpapi/server.go:1003` регистрирует `POST /api/v1/human/decide`, `internal/infra/httpapi/server.go:1245` `replay` отдаёт историю событий.

Перечисленное закрывает часть старых находок. Ниже осталось то, что эти правки не решили.

## Таблица находок

| Приоритет | Проблема | file:line, символ | Решение | Эффект |
| --- | --- | --- | --- | --- |
| P0 | Живая модель не достижима из CLI | `internal/tui/app.go:221` `RunLive`; `cmd/beacon/board_wire.go:81` `tui.Run`; `cmd/beacon/main.go:254` `"board"`; `cmd/beacon/main.go:286` `"up"`; `cmd/beacon/catalog.go:35` Usage у board | Один адаптер в `cmd/beacon` с тремя точками входа: `beacon tui`, `up --tui`, `board --live`. Все три вызывают `App.RunLive`. Порт объявляется в cmd без `net/http` | Один путь от клавиши к вердикту. Ребро `cmd -> internal/tui` уже разрешено в `layers.json:21`, новых рёбер ноль |
| P1 | Живой экран не знает verb-ов сервера в полном объёме | `internal/infra/httpapi/server.go:1291` `validCommands` содержит 9 verbs, включая `force_retry_merge` в строке 1294; `internal/tui/app.go:110` `Command` понимает 7; `internal/tui/socket.go:69` `DecisionCommands` содержит 8 | Список verbs берётся из одного места. Клавиши назначаются на `approve`, `unblock`, `pause`, `resume`, `cancel` и `force_retry_merge`. `context` добавляется в `app.go:110`: TUI-05 владеет `socket.go` и `up.go`, но не `app.go` | Подсказка совпадает с сервером. Неизвестный verb отвергается до POST |
| P1 | Нет одноклавишных действий оператора | `internal/tui/board.go:310` `Update` обрабатывает только навигацию и `:`; `internal/tui/board.go:385` `updateInput` требует ввод строки; `internal/tui/board.go:222` `SelectedWatch` есть, но к командам не подключён; `internal/tui/board.go:535` help обещает только `: команда` | Клавиши `a`, `u`, `p`, `r`, `c`, `f` вызывают `CommandFunc` с WatchID из `SelectedWatch`. Ответ сервера пишется в `statusLine` через `CommandMsg` | Одно нажатие вместо ввода команды. Отказ остаётся в строке статуса и не меняет снимок |
| P1 | `spawn` и `kill` в контракте отсутствуют | `internal/infra/httpapi/server.go:1291` `validCommands` без `spawn` и `kill`; `internal/tui/app.go:110` без них же | Клавиши не добавляются. Ограничение фиксируется в facts и в отрицательном контроле eval | UI не обещает commands, которых нет у сервера. Отдельный verb в домене не нужен |
| P1 | Роли и target воркеров есть в снимке, но не видны | `internal/tui/client.go:170` `Subscriber{Role, Target, Phase}`; `internal/tui/board.go:561` `fmt.Fprintf` печатает только `sub.ID` и `sub.Phase` | `watchLines` печатает `role` и `target` рядом с фазой | Панель воркеров показывает фазу и роль без нового поля в контракте |
| P2 | RSS есть в ответе сервера, но теряется клиентом | `internal/infra/httpapi/server.go:167` `Metrics json.RawMessage`; `cmd/beacon/mcp.go:858` `snap.Metrics`; `internal/infra/server/metrics.go:475` `RSSBytes json:"rssBytes"`; `internal/tui/client.go:138` `Snapshot` без поля `metrics`; `internal/tui/board.go:613` `memoryLines` печатает `RSS: нет данных в снимке` | Добавить необязательный разбор `metrics.rssBytes` в `internal/tui/client.go` и вывести число в `memoryLines`. TUI-04 меняет серверную сторону, но не владеет клиентом | Нижняя строка показывает VmRSS процесса. Новый сборщик не нужен |
| P2 | Для живого экрана нет текстового fallback | `internal/tui/board.go:661` `RenderFallback` печатает только карточки; `internal/tui/app.go:171` `Render` печатает снимок, но вызывается только из `internal/tui/app.go:127` `Run`; `cmd/beacon/board_wire.go:91` `boardHasTerminal` проверяет только stdin; `docs/tasks/TUI-07C.md:2` `dropped` | В адаптере проверять stdout через `os.Stdout.Stat`. Без TTY печатать `Render(snap)` вместо Bubble Tea | Pipe и CI получают тот же набор данных и код ноль. Дыра владения из TUI-07C закрывается слоем адаптера |

## Точки входа оператора

Три команды ведут в один и тот же раннер. `beacon tui` и `up --tui` отсутствуют в dispatch и каталоге: `cmd/beacon/main.go:254` содержит только `"board"`, `cmd/beacon/main.go:286` только `"up"`, `cmd/beacon/catalog.go:35` описывает board без `--live`. Флаг `--tui` в `cmd/beacon/up.go:149` `flag.NewFlagSet("up")` не объявлен, там есть `--interval`, `--once` и `--channels`.

Обвязка остаётся в слое cmd. `layers.json:21` уже разрешает импорт `internal/tui`, а `internal/tui/client.go:55` `Client` держит весь HTTP-транспорт. Новый файл адаптера должен попасть в OwnerFiles исполнителя при получении карточки, в этом пакете его нет на диске и в `owner_files` он не значится.

## Глаголы и горячие клавиши

Сервер объявляет девять verbs в `internal/infra/httpapi/server.go:1291` `validCommands`: восемь из старого аудита плюс `force_retry_merge` в строке 1294. Агентский канал перечисляет восемь в `internal/tui/socket.go:69` `DecisionCommands`, а текстовый парсер оператора семь в `internal/tui/app.go:110`. Расхождение не в рендере, а в трёх независимых списках.

Ручные `spawn` и `kill` в `validCommands` отсутствуют. Это ограничение намеренное: карточке нечего добавлять в UI до появления verb в домене и маршруте. Проверка в eval должна отправлять неизвестный verb и требовать отказа без изменения локального снимка.

## non-TTY

`internal/tui/board.go:661` `RenderFallback` покрывает статический список карточек и вызывается из `cmd/beacon/board_wire.go:75`. Живой снимок печатает `internal/tui/app.go:171` `Render`, но этого раннера нет ни в одном dispatch. Карточка TUI-07C помечена `dropped`, потому что детект stdout не имеет владельца. Настоящий пакет возвращает детект в адаптер, не меняя OwnerFiles dropped-карточки.

## Память

Цель 20-30 МБ RSS остаётся целью. Ограничители уже в коде: `internal/tui/client.go:31` `maxBodyBytes` равен 1 МиБ, `internal/tui/client.go:39` `eventReplayCount` равен 64, `internal/tui/board.go:43` `maxEventLines` равен 12, `internal/tui/socket.go:23` `outQueue` равен 16, `internal/tui/socket.go:26` `maxDedup` равен 256. Ни одно из этих чисел не измерялось этим агентом. Eval в карточке TU3-03 описывает замер `VmRSS` после первого кадра и после двухсот обновлений, а также проверку отсутствия процессов Node.js.

## Карточки пакета

- TU3-01, P0, task: три точки входа в живой экран. OwnerFiles `cmd/beacon/main.go`, `cmd/beacon/catalog.go`, `cmd/beacon/up.go`, `cmd/beacon/board_wire.go`.
- TU3-02, P1, task: одноклавишные verbs и полный список глаголов. OwnerFiles `internal/tui/board.go`, `internal/tui/app.go`.
- TU3-03, P1, eval: замер RSS, проверка verbs и fallback. OwnerFiles `docs/evals/tui-live-single-screen.md`.
- TU3-04, P2, task: RSS и роли воркеров в панелях живого экрана. OwnerFiles `internal/tui/client.go`, `internal/tui/board.go`.
- TU3-05, P2, task: текстовый fallback живого экрана. OwnerFiles `cmd/beacon/board_wire.go`, `internal/tui/app.go`.

OwnerFiles всех карточек существуют в дереве. Внутри каждой фазы файлы не пересекаются: P0 содержит только TU3-01, P1 содержит Go-файлы TU3-02 и eval-файл TU3-03, P2 содержит пары `{client.go, board.go}` и `{board_wire.go, app.go}`. Пересечение `board.go` между P1 и P2 и `app.go` между P1 и P2 допустимо по правилу «не пересекаются в фазе». Матрица слоёв не меняется.

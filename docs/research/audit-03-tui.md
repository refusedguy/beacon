# Аудит 03: один экранчик на чистом Go

Цель направления: живой экран Beacon в одном терминале без Node.js и браузера. Экран показывает активные слоты, воркеров и фазы, PR и вердикты, блокеры, очередь todo и deferred, хвост событий, RSS и GitHub points. Оператор управляет клавишами. Для неинтерактивного вывода есть текстовый fallback.

## Проверка premise

Код TUI уже лежит в дереве и не переписывается.

- `internal/tui/board.go:103` `type Model` и `internal/tui/board.go:357` `Run` дают интерактивный Bubble Tea экран по статическому списку карточек.
- `internal/tui/app.go:18` `type App` и `internal/tui/app.go:125` `Run` дают текстовый дашборд по живому снимку `/api/v1/snapshot` и SSE `/api/v1/events`.
- `internal/tui/client.go:55` `type Client` уже читает снимок, события и шлёт решения на `/api/v1/human/decide`.
- `internal/tui/socket.go:198` `Listen` уже поднимает Unix-сокет управления для агентов.
- `cmd/beacon/watch.go:499` `watchHTTPPort` и `cmd/beacon/watch.go:551` `renderHTTPSnapshot` уже показывают живой снимок через `httpapi.ProjectionClient` в режиме `beacon watch --live`.

Не подключено одно: интерактивный экран, который объединяет живой снимок, очередь и события в одном окне и принимает клавиши оператора. Статический board из `internal/tui` в dispatch не доходит. `cmd/beacon/board_wire.go:6` `installBoardAdapter` вызывает локальный `runBoard`, а не `tui.Run`. `cmd/beacon/main.go:245` dispatch оставляет команду `board` на этом раннере.

Конкурирующая работа: PR #1111 `fix/al7-915-recheck-5400179`, head `08147dabbbaa6b6eef764ed6a5874795777cb9d7`, открыт. Он добавляет ребро `cmd -> internal/tui` и вызывает `tui.Run` для статического board. Перед взятием любой карточки из этого аудита исполнитель обязан проверить состояние #1111 и не открывать второй PR на тот же адаптер. Карточка `docs/tasks/WIRING-915.md:46` имеет режим `needs_input` и ждёт решения владельца по тому же ребру.

Измерение до выбора: локальные `go test`, `go build` и `beacon gates` по правилам репозитория не запускались. Числа RAM в таблице являются целевыми оценками для исполнения, а не измеренным результатом. Порядок замера описан в разделе про память.

## Матрица слоёв

`tools/beacon-gates/gate/config/layers.json:15` задаёт явный список разрешённых рёбер, а не произведение рангов. `internal/tui` имеет ранг 2 в строке 9. Список `allowed` не содержит ни одного ребра в `internal/tui`. Следствия:

1. `cmd` не может импортировать `internal/tui`. Прямой вызов `tui.Run` из `cmd/beacon` ломает `gate/layers.go:382` `LAYER-FORBIDDEN-EDGE`.
2. Ни один существующий слой не может импортировать `internal/tui` без правки матрицы. Разрешение требует строгого убывания ранга по `gate/layers.go:78`.
3. Новое ребро должно использоваться реальным кодом. Иначе `gate/layers.go:398` поднимает `LAYER-UNUSED-RULE`. PR #1111 добавляет синтетический импорт в `gate/fixtures.go` именно по этой причине.
4. Бинарник `tools/beacon-gates/beacon-gates` хранит копию конфигурации через `go:embed` в `gate/gate.go:20`. Правка JSON без пересборки оставляет гейт и конфиг в разных состояниях.

Легальная схема подключения поэтому состоит из двух частей: тонкий адаптер в `cmd/beacon/tui.go` с портом и одно разрешённое ребро в матрице. Порт не отменяет требование ребра, но удерживает `net/http` и Bubble Tea вне cmd. `AGENTS.md:85` запрещает `cmd/beacon` использовать `net/http`, `os/exec`, `os.ReadFile` и `os.WriteFile`. `internal/tui/client.go:1` показывает, что весь транспорт уже вынесен в TUI-пакет.

Если владелец откажет в ребре, легальных вариантов внутри существующего модуля нет. Остаётся отдельный бинарник с собственной строкой в матрице, либо остановка на уже работающем `beacon watch --live`. Этот выбор фиксируется карточкой TUI-06, а не молчаливым обходом гейта.

## Таблица находок

Оценки RAM даны для одного процесса TUI. Токены означают размер подсказки для агента или дополнительный контекст протокола.

| Проблема | file:line, символ | Целевое решение | RAM / токены / надёжность |
| --- | --- | --- | --- |
| Интерактивный board недостижим из CLI | `cmd/beacon/board_wire.go:6` `installBoardAdapter`, `cmd/beacon/main.go:245` `"board"`, `internal/tui/board.go:357` `Run` | Порт в новом `cmd/beacon/tui.go`, адаптер в `board_wire.go` выбирает статический или живой режим, dispatch не меняется | +2-4 МБ на Bubble Tea, 0 токенов, один путь вызова вместо двух |
| Живой дашборд печатает текст, а не управляется клавишами | `internal/tui/app.go:125` `Run`, `internal/tui/app.go:169` `Render` | Передавать `Snapshot` в Bubble Tea `Model`, оставить `Render` как fallback | +1-2 МБ на модель, 0 токенов, ререндер только по revision из `app.go:68` `View` |
| Матрица запрещает `cmd -> internal/tui` | `tools/beacon-gates/gate/config/layers.json:15`, `gate/layers.go:116` `Allows`, `gate/gate.go:20` `go:embed` | Одно ребро `cmd -> internal/tui`, фикстура в `gate/fixtures.go`, пересборка бинарника, решение владельца из WIRING-915 | 0 МБ, 1 решение владельца, гейт сам проверяет unused rule |
| В работе уже есть PR #1111 на статический board | `docs/tasks/WIRING-915.md:2`, PR #1111 head `08147da` | Проверить PR перед claim, не открывать второй адаптер, вписать живой режим в уже существующий адаптер | 0 МБ, 0 токенов, убирает риск конфликтующего мержа |
| Два HTTP-клиента с разными именами полей | `internal/tui/client.go:129` `Snapshot`, `internal/infra/httpapi/client.go:177` `ClientSnapshot`, `internal/infra/httpapi/server.go:137` `Snapshot` | Один клиент за портом, второй оставить для `beacon watch --live`, не добавлять третий | -0.5 МБ за счёт удаления дубля декодирования, 0 токенов, одна точка контракта |
| GitHub points печатают ноль в знаменателе | `internal/tui/client.go:174` `Points.Limit`, `internal/infra/httpapi/server.go:71` `PointStats`, `cmd/beacon/mcp.go:846` `PointStats{Used, Budget}` | Рендерить `budget` или заполнять `limit` в провайдере, деление на ноль убрать | 0 МБ, 0 токенов, заголовок всегда показывает реальный бюджет |
| В строке воркера нет роли и target | `internal/tui/client.go:160` `Subscriber` | Добавить `role` и `phase` в проекцию, роли проверять локально, как `app.go:161` `knownBlockReasons` | +0.1 МБ, 0 токенов, панель воркеров показывает фазу и роль |
| Хвост событий пуст на холодном старте | `internal/tui/client.go:318` `openEvents`, `internal/infra/httpapi/server.go:951` `replay` | Добавить `?replay=N` к запросу событий и оставить `Last-Event-ID` для переподключения | Буфер ограничен серверным `maxReplay`, 0 токенов, события не теряются при старте |
| Очереди todo и deferred нет в снимке | `internal/infra/httpapi/server.go:715` маршруты, `cmd/beacon/board.go:48` `taskstore.LoadCards` | Добавить необязательный блок `queue` в снимок через провайдер, не читать файлы из cmd | +0.2 МБ на строки карточек, 0 токенов, старые клиенты не ломаются |
| Слоты оркестратора не видны в проекции | `GLOSSARY.md:85` `slot`, `internal/infra/httpapi/server.go:94` `OpsMetrics.ActiveTasks`, `cmd/beacon/mcp.go:828` `httpStateProvider.Snapshot` | Вывести ёмкость и занятые слоты в необязательный блок снимка | 0 МБ, 0 токенов, счётчик слотов совпадает с control plane |
| RSS есть в метриках, но не показан | `internal/infra/server/metrics.go:379` `ProcessRSSBytes`, `internal/infra/server/metrics.go:440` `metricsJSON.rssBytes`, `cmd/beacon/mcp.go:860` `snap.Metrics` | Декодировать `metrics.rssBytes` в general клиенте и вывести в нижней строке | 0 МБ, 0 токенов, не добавляется новый сборщик |
| Клавиши не связаны с human-decide | `internal/tui/board.go:200` `Update`, `internal/tui/app.go:100` `Command`, `internal/tui/client.go:237` `Decide`, `internal/infra/httpapi/server.go:989` список глаголов | Назначить клавиши на восемь verbs из `server.go:989`, показывать `Receipt` и отказ | 0 МБ, восемь коротких подсказок в help, неизвестный verb отвергается сервером |
| Управляющий сокет не знает pause и resume | `internal/tui/socket.go:42` константы `CmdAck`, `CmdCancel`, `CmdClaim`, `CmdRelease` | Расширить набор команд только если агенты должны повторять действия оператора | +0.1 МБ на таблицу команд, 0 токенов, не смешивать с hotkeys |
| Fallback есть только для карточек | `internal/tui/board.go:342` `RenderFallback`, `internal/tui/app.go:169` `Render` | Проверять режим stdout через `os.Stdout.Stat` и печатать текстовый снимок без TTY | 0 МБ, 0 токенов, pipe и CI продолжают работать |
| Транспорт и рендер не ограничены явным бюджетом | `internal/tui/client.go:31` `maxBodyBytes`, `internal/tui/socket.go:23` `outQueue`, `internal/tui/socket.go:26` `maxDedup` | Оставить один буфер снимка, кэш `View`, EventBus с фиксированным кольцом | Цель 20-30 МБ RSS, 0 байт Node.js, отказ SSE не роняет экран |

## Легальная обвязка

Схема для карточки TUI-01.

1. `cmd/beacon/tui.go` создаёт порт с тремя методами: читать снимок, подписываться на события, отправлять решение. Типы порта объявляются в cmd и не содержат `net/http`. Реализацию поставляет уже существующий клиент из `internal/infra/httpapi` или `internal/tui`.
2. `cmd/beacon/board_wire.go` заменяет адаптер `RegisterBoard`. Флаг `--live` выбирает живой раннер. Обычный вызов остаётся на `runBoard`, поэтому регрессия статического board исключена.
3. `cmd/beacon/main.go:245` оставляет dispatch на `boardRunner`. Каталог в `cmd/beacon/catalog.go:35` получает описание флага, но новая команда `tui` не нужна, если живой режим доступен через `beacon board --live`.
4. Конкретная Bubble Tea программа остаётся в `internal/tui`. Единственный новый импорт приходит из тонкого адаптера. В матрицу добавляется одно ребро `cmd -> internal/tui`.
5. Гейт получает фикстуру с реальным использованием ребра и пересобранный бинарник. Проверку выполняет CI на PR, локально `beacon gates` не запускается.

Альтернативу через `cmd/beacon/up.go` не стоит выбирать первым шагом. `up.go:89` `upDecisionAdapter` уже содержит резервный путь human-decide через store, но сам цикл `up` держит ReconcileLoop и Telegram. Подключение интерактивного экрана к этому процессу смешивает два жизненных цикла. Порт из `tui.go` можно использовать из `up.go`, если владелец выберет этот режим.

## Горячие клавиши

Сервер принимает ровно восемь глаголов в `internal/infra/httpapi/server.go:989`: `approve`, `reject`, `context`, `rework`, `pause`, `resume`, `unblock`, `cancel`. `internal/tui/app.go:100` `Command` понимает семь из них и псевдоним `ack` для approve. Слово `context` в команде отсутствует. `internal/tui/client.go:237` `Decide` приводит verb к нижнему регистру и отвергает пустой watchID.

Подсказка экрана может назначить клавиши `a`, `d`, `c`, `w`, `p`, `u`, `b`, `x` в том же порядке. Обновление экрана занимает `F5` или `Ctrl+R`, так как `app.go:106` уже использует `r` для refresh. Каждая клавиша вызывает `App.Decide`, затем `Refresh`, как делает `app.go:91`. Отказ сервера остаётся в строке статуса и не меняет локальное представление.

Ручные spawn и kill не входят в контракт `/api/v1/human/decide`. Добавить такие клавиши с помощью существующего endpoint нельзя. Карточка TUI-10 помечена `dropped` с этой причиной. Если оператору нужен такой контроль, сначала появится отдельный verb в домене и маршруте, а потом клавиша.

## Память и измерение

Цель 20-30 МБ RSS на один процесс. Что ограничивает размер:

- Один снимок в памяти. `internal/tui/client.go:31` ограничивает тело ответа 1 МиБ, `app.go:68` `View` пересобирает текст только при смене revision.
- Кольцо событий на сервере и буфер на клиенте. `internal/tui/socket.go:23` держит не больше 16 frames на агента, `maxDedup` в строке 26 равен 256.
- Bubble Tea и Lip Gloss в `go.mod:7` и `go.mod:8` не тянут Node.js и браузер. Дополнительный рантайм отсутствует.

Порядок замера для исполнителя карточки TUI-07: запустить живой экран под `--live`, снять `VmRSS` из `/proc/self/status` после первого кадра и после ста обновлений, записать оба числа. Отрицательный контроль: тот же сценарий без флага живого режима и вызов `--format` в pipe. Порог считается проваленным, если RSS больше 30 МБ или в выводе появляется строка процесса Node.js. Число 20-30 в таблице является целью, а не результатом этого аудита.

## Список карточек пакета

- TUI-01, P0, task: живой экран через порт в cmd. OwnerFiles `cmd/beacon/main.go`, `cmd/beacon/board.go`.
- TUI-02, P0, task: единая Bubble Tea модель по снимку и карточкам. OwnerFiles `internal/tui/app.go`, `internal/tui/board.go`.
- TUI-03, P0, task: один клиент и починка контракта points. OwnerFiles `internal/tui/client.go`, `cmd/beacon/watch.go`.
- TUI-04, P1, task: очередь, слоты, воркеры и RSS в снимке. OwnerFiles `internal/infra/httpapi/server.go`, `cmd/beacon/mcp.go`.
- TUI-05, P1, task: клавиши operator и verbs human-decide. OwnerFiles `internal/tui/socket.go`, `cmd/beacon/up.go`.
- TUI-06, P0, task: ребро матрицы и пересборка гейта после решения владельца. OwnerFiles `tools/beacon-gates/gate/config/layers.json`, `tools/beacon-gates/gate/gate_test.go`.
- TUI-07, P1, eval: замер RSS и проверка fallback. OwnerFiles `internal/tui/client_test.go`, `cmd/beacon/watch_w8b_test.go`.
- TUI-08, P1, doc: термины и обновление WIRING-915. OwnerFiles `docs/tasks/WIRING-915.md`, `GLOSSARY.md`.
- TUI-09, P1, dropped: третий HTTP-клиент. OwnerFiles `internal/infra/httpapi/client.go`. Причина: два клиента уже есть.
- TUI-10, P1, dropped: клавиши spawn и kill на human-decide. OwnerFiles `internal/domain/types.go`. Причина: verb отсутствует в контракте сервера.

OwnerFiles попарно не пересекаются и все существуют в `origin/main`. Новый файл `cmd/beacon/tui.go` описан в шагах TUI-01, но не указан в OwnerFiles, потому что его ещё нет на диске. Исполнитель добавляет его в OwnerFiles карточки при получении задачи.

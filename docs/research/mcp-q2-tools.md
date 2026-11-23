# Вопрос 2: каким инструментам MCP нужен opencode HTTP port

Отчёт подготовлен 2026-11-21. Код прочитан на коммите `2a3362f1` в `/mnt/projects/beacon-handoff/beacon-impl`. Режим только чтение. `tools.beacon.*` не вызывались, `go test`, `go build` и `beacon gates` не запускались. Поиск шёл через `rg` с явными каталогами.

## Короткий ответ

Ни один обработчик MCP-инструмента не вызывает `newOpencodePort`, `EnsureServer`, `sessionSpawner`, `deliveryTarget` или `liveSource`. Все девять контрактных инструментов работают через локальные порты: SQLite, store, доменные сервисы и файлы карточек. Пакет `internal/infra/mcp` не импортирует `internal/infra/opencode` и не импортирует `cmd/beacon`, поэтому вызвать порт из `package main` он не может.

opencode HTTP port используется в трёх местах вне обработчиков инструментов: стартовая и фоновая логика самого процесса `beacon mcp` (wake delivery и escalate), CLI-команды `spawn`, `deliver`, `watch --live` и подкоманда `registry sync`. Вызовы в фоновых циклах происходят до первого `tools/call`, но они не принадлежат ни одному инструменту. Это различие важно для вопроса 1: старт процесса создаёт порт, а инструмент его не создаёт.

Дополнительно к девяти контрактным инструментам производственная сборка `beacon mcp` подключает десятый, `beacon_wake`. Он ближе всего к wake delivery по смыслу, но в этой сборке ему не передан отправитель. Порт `WakeSender` равен nil, поэтому инструмент честно отвечает `delivery=unsupported` и не делает HTTP-запроса.

## 1. Реестр инструментов

Единственный источник истины это таблица `toolDescriptors` в `internal/infra/mcp/server.go:133-224`. Из неё выводятся все остальные перечни:

- `ToolNames`, `server.go:229-235`;
- `toolAnnotations`, `server.go:294-301`;
- `outputSchemaFactories`, `internal/infra/mcp/schema.go:332-336`.

`NewServerWithDeps` (`server.go:632-670`) сначала регистрирует девять заглушек в цикле на строке 644, затем заменяет каждую реальным обработчиком в цикле на строке 666.

| Инструмент | Строка дескриптора | Обработчик |
| --- | --- | --- |
| `beacon_watch_pr` | 133-144 | `NewWatchPRHandler` + `AttachWatchPRWithConfig` |
| `beacon_wait_result` | 145-153 | `NewWaitResultServiceFromDeps` + `AttachWaitResult` |
| `beacon_get_result` | 154-164 | `NewGetResultHandler` + `AttachGetResult` |
| `beacon_list_running` | 165-173 | `attachListRunning` + `NewListRunningHandler` |
| `beacon_ack` | 174-187 | `NewAckHandler` + `AttachAck` |
| `beacon_cancel_watch` | 188-196 | `NewCancelWatcher` + `RegisterCancelWatch` |
| `beacon_stats` | 197-205 | `attachStats` + `NewStatsHandler` |
| `beacon_defer_wiring` | 206-214 | `NewDeferWiringHandler` + `AttachDeferWiring` |
| `beacon_task_claim` | 215-223 | `NewTaskClaimHandler` + `attachTaskClaim` |

Инструменты вне контрактного списка:

- `beacon_wake`, константа в `tool_wake.go:115`. Производственная сборка подключает его через `AttachWake` в `cmd/beacon/mcp.go:436`.
- `t3_thread_read`, `tool_thread_read.go:20`. Подключается только по отдельному `AttachThreadRead`, `server.go:748`.
- `beacon_claim`, `beacon_release`, четыре инструмента worktree-аренды и `beacon_extend_holder_lease`, `tool_claim.go:79-80`, `599-602`, `1048`. Подключается через `AttachClaim`, `AttachWorktreeLease`, `AttachHolderLease`.
- `tasks_get`, `tool_get_result.go:1711`. Подключается через `AttachTasksGet`.
- Удалённые инструменты из `AttachRemoteTools`, `cmd/beacon/mcp.go:478-484`. Они делают HTTP-запрос к серверу Beacon по адресу из `connection.json`, а не к opencode.

## 2. Порты зависимостей

Структура `Dependencies` определена в `server.go:396-472`. Все её поля локальные: `Store`, `CI`, `Stamper`, `Writer`, `Events`, `Source`, `Snapshot`, `Clock`, `Blockers`, `SharedCIWaiter`, `After`, `CallScopes`, `PrecheckJournal`, `Continuations`, `DeferWiring`, `TaskClaimStore`, `ChildThreads`, `ParentNotifier`, `Version`. Порта opencode в структуре нет.

`buildMCPDependencies` (`cmd/beacon/mcp.go:542-591`) связывает эти порты с одним дескриптором SQLite:

- `Store: db`, `Stamper`, `Writer`, `Events`, `Blockers` и `DeferWiring` пишут в тот же `db`;
- `Source` это `mcp.NewDBVerdictSource(db, ...)`, строка 554;
- `SharedCIWaiter` это адаптер над `store.SharedCIWait`, `mcp.go:490-492`;
- `Snapshot` читает `db.LiveWatches()`, `mcp.go:567-589`;
- `PrecheckJournal` это `taskcli.NewService` над каталогом состояния.

`CI` в этой сборке не задан. Комментарий в `mcp.go:502-503` прямо говорит, что производственного адаптера CI в дереве нет, поэтому `watch_pr` требует `headSHA` от вызывающего.

Поиск по каталогу `internal/infra/mcp` нашёл только два упоминания `net/http` в нетестовом коде: `tool_get_result.go:620` и `client.go:18`. Первое обращается к GitHub REST за текстом failed check, второе реализует удалённый клиент MCP. Ни одно из них не обращается к opencode.

## 3. Разбор по инструментам

### beacon_watch_pr

Регистрация в `server.go:139-143`. Производственная точка это `newMCPServer` в `cmd/beacon/mcp.go:435`, где вызывается `AttachWatchPRWithConfig`.

`WatchPRHandler.Register` (`tool_watch_pr.go:388`) проверяет вход, вызывает `resolveHead` (418), `closeSuperseded` (429), строит `domain.Watch` и пишет через `h.fanout.Subscribe` (496) и `h.store.SaveWatch` (545). `resolveHead` (701-710) при отсутствии `headSHA` читает `h.ci.PRState`, то есть GitHub, а не opencode. `WatchSessionRegistry` в `meta.go:497-602` хранит отображение session ID в запись с полем `Target`. `RegisterAndBind` (`meta.go:609-620`) после регистрации записывает эту связь в память.

Поле `notify.target` это непрозрачная строка адреса или session ID в `domain.Subscriber.Target`, `tool_watch_pr.go:486-494`. Инструмент сохраняет её, но не делает по ней запроса. Фактическая доставка wake позже выполняется reconciler-ом через wake registry, описанной в разделе 4.

Вывод: только store, CI GitHub, blockers, часы и in-memory registry. opencode port не нужен.

### beacon_wait_result

Регистрация в `server.go:150-152`. Сервис строится из тех же портов: `Writer` (SQL), `Source` (DB verdict poller), `SharedCIWaiter` (SQLite gate), `CallScopes` (in-memory capability context), `PrecheckJournal` (файлы) и `Continuations`. Ни один из них не является HTTP-клиентом opencode.

Вывод: локальные порты. opencode port не нужен.

### beacon_get_result

Регистрация в `server.go:159-163`. `GetResultHandler` читает Watch и Obligation из store и ставит отметку прочитанного через `Stamper`.

Единственный сетевой код инструмента это `envFailureExcerptSource` (`tool_get_result.go:617-711`). Он читает `BEACON_GITHUB_TOKEN`, обращается к `api.github.com` за check runs и annotations и не имеет отношения к opencode. Без токена источник равен nil и сетевого вызова нет вовсе.

Вывод: store, SQLite и опциональный GitHub REST. opencode port не нужен.

### beacon_list_running

Регистрация в `server.go:170-172`, реальная установка в `attachListRunning` (`server.go:680-695`). `NewListRunningHandler` читает только `SnapshotReader`. Production snapshot строится из `db.LiveWatches()`.

Вывод: чистое чтение SQLite. opencode port не нужен. Это же подтверждено в `docs/research/mcp-q1-startup.md`, раздел 4.

### beacon_ack

Регистрация в `server.go:179-186`. `NewAckHandler` получает `Store`, `Stamper`, `Clock`, `Blockers` и `Continuations`. Структура обработчика определена в `tool_ack.go:384-402`; в ней нет поля отправителя или уведомителя.

`decision=approve` и `decision=reject` меняют состояние Watch через `DecisionRegistry.Decide` (`tool_ack.go:227-247`) и вызывают переданный локальный `resume`. Слова «resumes a stopped agent» в описании инструмента означают доменный переход в store, а не POST в live-сессию. `blocked:not_mine` пишет `BlockerIncident` и берёт session ID из `_meta`, что тоже не требует связи с сессией.

Вывод: store, SQLite и in-memory счётчики. opencode port не нужен.

### beacon_cancel_watch

Регистрация в `server.go:193-195`. `NewCancelWatcher(deps.Store, deps.Events, deps.Clock)` пишет изменение в store и событие отмены в SQL event log.

Вывод: локальные порты. opencode port не нужен.

### beacon_stats

Регистрация в `server.go:202-204`, реальная установка в `attachStats` (`server.go:703-718`). Читает тот же `SnapshotReader`, что и `beacon_list_running`, и подставляет версию сборки в описание.

Вывод: чистое чтение SQLite. opencode port не нужен.

### beacon_defer_wiring

Регистрация в `server.go:211-213`. `NewDeferWiringHandler` получает только `DeferWiring`. В production это `newDurableDeferredStore(db)`, `mcp.go:566` и `cmd/beacon/deferred.go:57`. Инструмент пишет в SQLite и возвращает готовый markdown в ответе.

Вывод: локальный store. opencode port не нужен.

### beacon_task_claim

Регистрация в `server.go:216-222`. `TaskClaimHandler` работает через порт `TaskClaimStore` (`tool_task_claim.go:61-78`). Стандартная реализация `FileTaskClaimStore` (`tool_task_claim.go:112-150`) читает markdown-карточки и держит блокировку в локальном файле. Ни HTTP, ни opencode в ней нет.

Отдельный факт: `buildMCPDependencies` не заполняет `TaskClaimStore`. Комментарий в `server.go:455-457` говорит, что при nil порт инструмент остаётся зарегистрированным, но отвечает `PERSIST_FAILED`. Это не меняет ответа про opencode: в любом варианте инструмент локальный.

### beacon_wake (десятый инструмент производственной сборки)

`newMCPServer` (`cmd/beacon/mcp.go:430-438`) вызывает `AttachWake` с результатом `newMCPWakeHandler` (446-452). Тот строит `mcp.NewWakeHandler(nil)` и вешает только `WithRegistryGuard(newWakeRegistryGuard(db, ...))`. Guard читает статус исполнителя из SQLite, `cmd/beacon/registry_sync.go:367-409`.

`WakeHandler` хранит `sender WakeSender` (`tool_wake.go:635-648`). `NewWakeHandler` (`tool_wake.go:701-707`) допускает nil и помечает это как `DeliveryUnsupported` во время вызова. Комментарий в `mcp.go:440-445` говорит то же самое: в production отправитель доставки не подключён, и wake, прошедший проверку registry, всё равно сообщает об неподдерживаемой доставке.

Вывод: инструмент зарегистрирован, но opencode port к нему не подключён. Вызов не порождает HTTP-запрос и возвращает `delivery=unsupported`.

### Остальные инструменты вне контракта

`t3_thread_read` читает историю тредов через `ThreadHistorySource`, инструменты аренды в `tool_claim.go` работают с локальными store и файлами блокировок, `tasks_get` работает с `ProtocolTaskStore`. Ни один не упоминает opencode port. Удалённые инструменты при `BEACON_MCP_REMOTE` (`cmd/beacon/mcp.go:1169-1178`) уходят на сервер Beacon через `mcp.NewRemoteClient`, а не на opencode.

## 4. Где реально вызывается opencode HTTP port

Нетестовые вызовы `newOpencodePort()` ровно три, все в `cmd/beacon`.

1. `cmd/beacon/main.go:131-137`, `wireOpenCodePorts`. Результат уходит в `installOpenCodePorts` (`main.go:145-156`). Там клиент по type assertion становится `sessionSpawner` для `beacon spawn` и `deliveryTarget` для `beacon deliver`, а также источником `liveSource` для `beacon watch --live`. `EnsureServer` ставится внутри `newOpencodePort` на `opencode.go:128` и вызывается из `resolveOpencodeConfig` (`opencode.go:103-105`).
2. `cmd/beacon/mcp.go:1516-1542`, `opencodeWakePorts`. Функция превращает `*opencode.Client` в `wake.PromptFunc` и `wake.SessionActiveFunc`. Её вызывает `reconcileWaker` (`mcp.go:1547-1556`), откуда порт попадает в `startHTTPAPI` (906), `makeEscalator` (1586), `runLadderPass` (1664) и `ReconcilePass` (1732). `ladderPointerSender` (`mcp.go:1562-1576`) рассылает короткие указатели через тот же wake registry. Это фоновые циклы `beacon mcp`, которые Q1 относит к стартовому коду.
3. `cmd/beacon/registry_sync.go:292-296`, `registrySyncOpenSource`. Подкоманда `registry sync` читает `session.list`. Комментарий в строках 286-289 прямо говорит, что у команды нет порта prompt или создания сессии.

`EnsureServer` определён в `internal/infra/opencode/server.go:390`. До него доходят только через `resolveOpencodeConfig`. Ни один обработчик MCP до этой функции не доходит.

Если сервис opencode не настроен, `newOpencodePort()` возвращает ошибку, `opencodeWakePorts()` возвращает nil-порты (`mcp.go:1517-1520`), и wake deliverer переходит на свой CLI-путь, как сказано в комментарии на строках 1513-1515. MCP-инструменты при этом продолжают работать на SQLite.

## 5. Итоговые списки

### Инструменты, которым opencode port не нужен

Все инструменты, зарегистрированные в `beacon mcp`:

| Инструмент | Почему не нужен port |
| --- | --- |
| `beacon_watch_pr` | store, blockers, часы, GitHub PRState, in-memory binding session ID |
| `beacon_wait_result` | SQL writer, DB verdict source, SQLite gate, in-memory capability scope |
| `beacon_get_result` | store и stamper; опциональный HTTP только к GitHub REST |
| `beacon_list_running` | чтение SQLite snapshot |
| `beacon_ack` | store, blockers, DecisionRegistry, ContinuationRegistry |
| `beacon_cancel_watch` | store и SQL event log |
| `beacon_stats` | чтение SQLite snapshot |
| `beacon_defer_wiring` | SQLite и генерация markdown карточки |
| `beacon_task_claim` | порт карточек; стандартная реализация читает markdown и локальный lock file |
| `beacon_wake` | зарегистрирован в production, но `WakeSender` равен nil; доставка честно сообщает `unsupported` |

### Инструменты, которым opencode port нужен

Список пуст. Ни один обработчик инструмента не вызывает `newOpencodePort`, `EnsureServer`, `sessionSpawner`, `deliveryTarget` или `liveSource`.

Практическая оговорка. Сам процесс `beacon mcp` открывает opencode port в фоновом reconcile-цикле, а CLI-команды `spawn`, `deliver`, `watch --live` и `registry sync` открывают его в своих путях. Если задача состоит в том, чтобы инструменты MCP работали без сервиса opencode, ответ положительный на уровне обработчиков. Фоновая доставка wake при этом деградирует до CLI-пути, а не падает.

## Что не проверено

- Прямой трассировки сетевых запросов во время исполнения нет. Выводы получены из кода на коммите `2a3362f1`.
- Фактический вывод `tools/list` не снимался. Десятый инструмент `beacon_wake` выведен из кода `newMCPServer`, а не из ответа раннего сервера.
- Нулевое значение `TaskClaimStore` в `beacon mcp` выведено из списка полей `buildMCPDependencies` и комментария в `server.go:455-457`. Сервер не запускался.
- Доступность `service.json` и список живых процессов opencode в этом отчёте не проверялись. Эти факты рассмотрены в `docs/research/mcp-q1-startup.md` и `docs/research/process-leak-mechanism.md`.

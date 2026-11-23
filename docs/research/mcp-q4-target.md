# Вопрос 4: целевая архитектура чистого MCP-сервера

Дата: 2026-11-21. Репозиторий `/mnt/projects/beacon-handoff/beacon-impl`, HEAD `2a3362f1`. Режим только чтение. Код, конфиги и карточки не менялись. Тесты, сборки, гейты и инструменты beacon не запускались. Поиск шёл через `rg` с явными каталогами.

Файла `mcp.lua` в репозитории нет. Регистрацию MCP для OpenCode держат `.opencode/plugins/beacon-project/mcp.js:9-20` и `opencode.json:3-13`. Оба задают локальный stdio-сервер с командой `.opencode/bin/beacon mcp`.

## Короткий ответ

Транспорт и набор инструментов уже совпадают с тем, что описал владелец. Процесс `beacon mcp` уже общается с OpenCode по stdio и отдаёт девять инструментов через `tools/list` и `tools/call`. При этом старт процесса делает лишнюю работу. Он поднимает фоновый цикл, HTTP API, webhook, сетевой GitHub-провайдер и цепочку wake-адаптеров. Часть этой цепочки вызывает `EnsureServer` и может запустить `opencode serve`. Для чистой схемы достаточно отключить вызовы в `runMCP` и убрать один ensurer в `cmd/beacon/opencode.go`. Удалять ядро MCP не нужно.

## 1. Сопоставление слов владельца с кодом

Владелец описал идеальную схему так. MCP работает в том же режиме, агенты вызывают доступные инструменты, других механизмов нет, `opencode serve` не используется.

| Тезис владельца | Что делает код на `2a3362f1` | Вердикт |
| --- | --- | --- |
| MCP крутится как сейчас | `.opencode/plugins/beacon-project/mcp.js:9-20` и `opencode.json:3-13` регистрируют локальный stdio-сервер. `cmd/beacon/mcp.go:454-458` запускает `newMCPServer(deps).Run(ctx, &mcpsdk.StdioTransport{})`. | Совпадает. |
| Агенты стучатся через доступные тулзы | `internal/infra/mcp/server.go:133-224` задаёт девять дескрипторов инструментов. Порты обработчиков перечислены в `internal/infra/mcp/server.go:396-459`. Обработчики читают и пишут SQLite через `store`, карточки через `taskstore`, вердикты через `DBVerdictSource`. | Совпадает. Ни один из девяти обработчиков не вызывает OpenCode HTTP. |
| Никаких дополнительных вещей | `cmd/beacon/mcp.go:1127-1243` читает `connection.json`, делает precheck, поднимает GitHub-батч, берёт poller lock, запускает `ReconcileLoop`, HTTP API и webhook. Всё это происходит до первого `tools/call`. | Не совпадает. |
| opencode serve не нужен вообще | `cmd/beacon/main.go:121` безусловно вызывает `wireOpenCodePorts()`. `cmd/beacon/opencode.go:128` ставит fallback-стартер `opencode.EnsureServer`. Стартовый reconcile-цикл вызывает его через wake-адаптер `cmd/beacon/mcp.go:1517`. `internal/infra/opencode/server.go:103-128` запускает `opencode serve`. | Не совпадает. |

### Где именно старт `beacon mcp` доходит до `EnsureServer`

1. `cmd/beacon/main.go:116-125` вызывает `wireOpenCodePorts()` для любой команды.
2. `cmd/beacon/main.go:131-137` вызывает `newOpencodePort()`. Это первый вызов в процессе.
3. `cmd/beacon/opencode.go:118-129` читает конфиг, строит HTTP-клиент и на строке 128 ставит `SetServerEnsurer(serverEnsurerFunc(opencode.EnsureServer))`. При первом вызове ensurer ещё nil, поэтому `resolveOpencodeConfig` на строках 102-106 читает только `service.json`. Спавна нет.
4. `cmd/beacon/main.go:263` передаёт управление `mcpRunner`. `cmd/beacon/mcp.go:392-398` вызывает `runMCP`.
5. `cmd/beacon/mcp.go:1220` вызывает `runReconciler`. Тот на строке 1797 вызывает `onPollerOwner`, а на строках 1801-1804 запускает `ReconcileLoop`.
6. `onPollerOwner` из `cmd/beacon/mcp.go:1214-1217` вызывает `startHTTPAPI`. Тот на строке 906 кладёт `reconcileWaker(cfg.db)` в `humanDecisionSink`.
7. `reconcileWaker` на `cmd/beacon/mcp.go:1547-1556` вызывает `opencodeWakePorts` на строке 1549. Функция `opencodeWakePorts` на строках 1516-1542 вызывает `newOpencodePort()` на строке 1517. Это второй вызов, и ensurer уже установлен.
8. `cmd/beacon/opencode.go:103-105` вызывает `installedServerEnsurer.EnsureServer`.
9. `internal/infra/opencode/server.go:390-429` сначала проверяет `ReadServeState()` на строке 394, затем `ReadConfig()` на строке 397, и только после этого выделяет порт на строке 401, спавнит на строке 410 и пишет состояние на строке 424.
10. `startServeProcess` на `internal/infra/opencode/server.go:103-128` собирает команду `exec.Command("opencode", "serve", "--hostname", ..., "--port", ...)` и запускает её на строке 119.

`serveState` это файл `opencode-serve.json`. Имя определено в `internal/infra/opencode/server.go:41-43`, путь в `218-224`, запись в `252-269`, чтение в `234-250`, очистка в `271-281`. `cmd/beacon/gc.go:31-36` подключает чтение и очистку этого файла к команде `beacon gc`.

`reconcileWaker` вызывается также в `cmd/beacon/mcp.go:1586`, `1664` и `1732`. Эти вызовы живут в фоновом цикле и тоже ведут к `newOpencodePort()`. Поэтому удаление только HTTP API не убирает вызов.

Фактический spawn происходит не всегда. `EnsureServer` переиспользует живой запись в `opencode-serve.json` или `service.json`. Новый процесс поднимается только когда обоих нет. Вызов `newOpencodePort()` при старте происходит в любом случае у процесса, который выиграл `beacon-poller.lock`.

### Что ещё нарушает тезис «никаких дополнительных вещей»

- `cmd/beacon/mcp.go:1137-1144` читает `connection.json`. Файл пишет HTTP API в `internal/infra/httpapi/server.go:623-646`.
- `cmd/beacon/mcp.go:1152-1158` запускает precheck init event. При ошибке `runMCP` вообще не поднимает инструменты.
- `cmd/beacon/mcp.go:1169-1178` содержит ветку `BEACON_MCP_REMOTE`. Она превращает stdio-процесс в тонкий HTTP-клиент.
- `cmd/beacon/mcp.go:1189` создаёт GitHub-батч. `newGitHubBatch` на строках 1434-1455 обращается к GitHub и к счётчику очков при старте.
- `cmd/beacon/mcp.go:1214-1240` поднимает HTTP API, webhook, таймеры лестницы и poller. Сокеты слушаются до первого вызова инструмента.
- `cmd/beacon/mcp.go:436` регистрирует `beacon_wake`. Инструмент не входит в `ToolNames`, но доступен через `tools/call`. `newMCPWakeHandler` на строках 446-452 строит обработчик без sender, поэтому вызов уже отвечает отказом доставки.

`BEACON_MCP_REMOTE` отдельно стоит отметить. Клиент в `internal/infra/mcp/client.go:29-38` обращается к пути `/mcp`. Продакшн-сервер этого пути не регистрирует. Список маршрутов в `internal/infra/httpapi/server.go:685-694` содержит только `/api/v1/*`. Обработчик `NewMCPHTTPHandler` из `internal/infra/server/mcp.go:33-49` используется только в тестах. Документ `docs/tasks/BS.43.md` прямо называет эту функциональность дублем. Удалённый режим в текущем main рабочим не является.

## 2. Целевая архитектура

Один процесс `beacon mcp`, запущенный OpenCode как stdio-сервер.

```text
OpenCode
  |  stdio: .opencode/bin/beacon mcp
  v
beacon mcp (один процесс, без дочерних процессов и без слушающих сокетов)
  +-- mcpsdk.Server
  |     +-- tools/list: девять имён из internal/infra/mcp/server.go:133-224
  |     +-- tools/call: обработчики из internal/infra/mcp/*
  +-- mcp.Dependencies
  |     +-- *store.DB, SQLite beacon.db
  |     +-- stamper, writer, events, verdict source
  +-- карточки docs/tasks через taskstore и FileTaskClaimStore
```

Что остаётся без изменений:

- Регистрация сервера в `.opencode/plugins/beacon-project/mcp.js` и `opencode.json`.
- `mcpStateDBName` в `cmd/beacon/mcp.go:110`, `stateDir()`, `openProcessStore` в `530-535`.
- `buildMCPDependencies` в `542-591` вместе с портами, которые не требуют сети, и локальными адаптерами `ladderStamper`, `ladderWaitWriter`, `ladderCounts`.
- `newMCPServer` в `427-438` без `AttachWake`, `serveMCP` в `454-458`.
- Ядро `internal/infra/mcp`. Его обработчики уже работают с портами и не знают про OpenCode HTTP.

Что именно отключить в коде.

Первое, обязательное изменение это тело `runMCP` в `cmd/beacon/mcp.go:1127-1243`. После замены процесс открывает SQLite, строит зависимости и запускает stdio.

```go
func runMCP(ctx context.Context, cli ...*CLI) error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	db, err := openProcessStore(dir)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	deps := buildMCPDependencies(db, dir, nil)
	defer stopVerdictSource(deps.Source)
	return serveMCP(ctx, deps)
}
```

Параметр `cli` можно оставить ради совместимости с тестами. В теле он не используется. В вызове `buildMCPDependencies(db, dir, nil)` третий аргумент nil, поэтому `beacon_stats` показывает стандартный бюджет вместо реальных очков GitHub. Это ожидаемая цена отказа от сетевого провайдера на старте.

Одной этой замены достаточно, чтобы убрать из пути MCP все вызовы `newOpencodePort`, `EnsureServer`, `runReconciler`, `startHTTPAPI`, `startWebhookReceiver` и `newGitHubBatch`. Функции остаются в файле, потому что их используют `beacon daemon` и `beacon up`. В пути MCP нужно убрать вызовы, а не удалять общие функции из исходного файла.

Второе изменение это `cmd/beacon/mcp.go:436`. Строка `mcp.AttachWake(s, newMCPWakeHandler(deps))` удаляется. Функция `newMCPWakeHandler` в `440-452` после этого не имеет вызывающих и тоже удаляется. Инструмент `beacon_wake` не входит в `ToolNames`, поэтому контракт девяти имён не меняется.

Третье изменение на уровне всей бинарной программы это `cmd/beacon/opencode.go:128`. Строка `SetServerEnsurer(serverEnsurerFunc(opencode.EnsureServer))` убирается. После неё `resolveOpencodeConfig` всегда читает `service.json`. Других производственных установщиков ensurer нет. `opencode.Client` не реализует `serverEnsurer`, поэтому ветка `cmd/beacon/main.go:156-158` в prod ничего не ставит. Удаление одной строки прекращает спавн `opencode serve` для всех команд, а не только для MCP.

Четвёртое, необязательное изменение это `cmd/beacon/main.go:121`. Вызов `wireOpenCodePorts()` можно пропустить для команды `mcp`. Тогда процесс MCP вообще не строит HTTP-клиент OpenCode. Инструменты этот клиент не используют.

Что можно удалить после упрощения `runMCP`, если нужна физическая уборка кода:

- `cmd/beacon/mcp.go:296-365` типы и чтение `connection.json`, `mcpConnector`, `activeMCPConnection`.
- `cmd/beacon/mcp.go:203-229`, `367-389`, `400-425` код precheck и его причин.
- `cmd/beacon/mcp.go:460-484` переменная `BEACON_MCP_REMOTE` и `serveMCPRemote`.
- `cmd/beacon/mcp.go:606-1009` сборка HTTP API и webhook. Функции `startHTTPAPI` и `startWebhookReceiver` после перезаписи `runMCP` не вызываются.
- `internal/infra/server/mcp.go:14`, `33-66` HTTP-обработчик `/mcp`. В prod у него нет вызывающих.
- `internal/infra/mcp/client.go:29-380` remote-клиент без производственного вызывающего.

Нельзя удалять как мёртвые следующие куски, пока живут `beacon daemon` и `beacon up`:

- `cmd/beacon/mcp.go:115-193` совместимые seam и старый daemon-цикл.
- `cmd/beacon/mcp.go:1245-1400` poller lock и метрики.
- `cmd/beacon/mcp.go:1402-1461` `reconcileConfig` и `newGitHubBatch`.
- `cmd/beacon/mcp.go:1477-1511` `orchestratorProjectKey` и `orchestratorIDSource`.
- `cmd/beacon/mcp.go:1558-1810` `ladderPointerSender`, `makeEscalator`, `ReconcilePass`, `ReconcileLoop`, `runReconciler`.
- `cmd/beacon/mcp.go:1463-1475` `projectRootForConfig`. Он нужен `AttachWatchPRWithConfig` на строке 435.

## 3. Что произойдёт с инструментами

Ни один из девяти объявленных инструментов не обращается к HTTP OpenCode. Поиск по каталогу `internal/infra/mcp` не нашёл там `newOpencodePort` или `EnsureServer`. Удаление wake-адаптера поэтому не убирает ни одного имени из `tools/list`.

| Инструмент | Источник данных | Зависимость от OpenCode HTTP | Эффект чистого сервера |
| --- | --- | --- | --- |
| `beacon_watch_pr` | `store`, `headSHA` от вызывающего | Нет | Работает. `deps.CI` в `buildMCPDependencies` не заполнен, поэтому агент обязан передавать `headSHA`. Ограничение `tool_watch_pr.go:705-707` уже действует. |
| `beacon_wait_result` | `store` и `DBVerdictSource` | Нет | Читает вердикт из SQLite. Вердикт пишет фоновый reconciler. Без него ожидание уйдёт в timeout. См. риск ниже. |
| `beacon_get_result` | `store`, `stamper`, опционально GitHub REST для excerpt | Нет | Работает. `envFailureExcerptSource` в `tool_get_result.go:612-711` ходит в GitHub, а не в OpenCode. |
| `beacon_list_running` | `SnapshotReader` | Нет | Работает без изменений. |
| `beacon_ack` | `store`, decision registry | Нет | Состояние Watch меняется в SQLite. Указатель агенту сейчас отправляет reconciler, но возобновление агента читается через `wait_result` из того же store. |
| `beacon_cancel_watch` | `store`, event log | Нет | Работает без изменений. |
| `beacon_stats` | `SnapshotReader`, счётчики ladder | Нет | Работает. Поля очков и `runtime_stats` становятся менее точными без GitHub-батча и poller-а. |
| `beacon_defer_wiring` | SQLite через `newDurableDeferredStore` | Нет | Работает. Строка карточки возвращается в ответе, файл закрывает reconciler. |
| `beacon_task_claim` | `deps.TaskClaimStore`, markdown-карточки | Нет | Сейчас уже не работает в prod. `buildMCPDependencies` в `mcp.go:546-590` не заполняет `TaskClaimStore`, поэтому обработчик отвечает `PERSIST_FAILED`. |

Отдельно нужно решить, что делать с `beacon_task_claim`. Удалять его нельзя, он входит в контракт из девяти имён. Переводить на локальный store уже не нужно, адаптер `mcp.FileTaskClaimStore` описан в `internal/infra/mcp/tool_task_claim.go:112-118`. Достаточно заполнить поле в `buildMCPDependencies`, например `&mcp.FileTaskClaimStore{TasksDir: filepath.Join(projectRootForConfig(), "docs", "tasks"), StateDir: dir}`. Это независимо от темы `opencode serve`.

`beacon_wake` не входит в `ToolNames`. Его регистрация на `cmd/beacon/mcp.go:436` не даёт рабочей доставки, потому что `newMCPWakeHandler` получает nil sender. Рекомендация удалить регистрацию и обработчик, а не оставлять ленивый вариант. Ленивый `EnsureServer` внутри sender только отложит спавн. Такой вывод уже зафиксирован в `docs/research/mcp-spawn-rootcause.md:69`.

Команды `beacon spawn`, `beacon deliver`, `beacon watch --live`, `beacon attach` и `beacon registry sync` используют HTTP OpenCode. Это не инструменты MCP. После удаления строки `cmd/beacon/opencode.go:128` они продолжат работать через `service.json`. Если файла нет, они честно сообщат об отсутствии конфигурации. Новый пустой сервер и раньше не мог доставить сообщение в сессию агента, потому что в нём нет сессий. Этот аргумент приведён в `docs/research/mcp-spawn-rootcause.md:37` и `:53`.

## 4. Риск, который нужно проверить до правки

Владелец сказал «MCP крутится как сейчас». Сейчас вердикты для `beacon_wait_result` пишет встроенный reconciler. Он живёт в том же процессе `beacon mcp` и вызывается из `cmd/beacon/mcp.go:1220`. Источник `DBVerdictSource` в `internal/infra/mcp/verdict_source.go:26-91` только наблюдает за SQLite. Сам он вердикт не вычисляет.

Если убрать вызов `runReconciler`, watches останутся в статусе `watching` до timeout. Ни один из девяти инструментов не возьмёт на себя оценку PR. Есть три варианта.

1. Держать отдельный процесс `beacon daemon`. Он запускает тот же цикл в `cmd/beacon/daemon.go:398` и работает с тем же SQLite. Минус: появляется второй процесс, что противоречит фразе «никаких дополнительных вещей».
2. Оставить в `beacon mcp` узкий фоновый цикл только для оценки PR. Это сохранит вердикты, но вернёт сетевые вызовы GitHub и фоновую горутину.
3. Перенести оценку прямо в обработчики инструментов. Нужен реальный `domain.CIProvider` в `deps.CI`, а сейчас он nil. Это самая большая правка.

Рекомендация по коду в этом отчёте описана для варианта 1. Если владелец настаивает на одном процессе без фонового цикла, нужно явно принять вариант 3 или согласиться с тем, что цикл watch wait перестанет закрываться.

Второй риск это число процессов. OpenCode запускает локальный stdio-сервер заново для вызовов инструментов, что зафиксировано в `docs/research/process-leak-rootcause.md:29-34`. Чистый сервер не убирает это число. Он убирает только дочерние `opencode serve`, слушающие сокеты и фоновые циклы. Один долгоживущий процесс потребовал бы другой транспорт, а владелец его отверг.

## 5. Точный список файлов и строк для минимальной правки

| Файл | Строки | Действие |
| --- | --- | --- |
| `cmd/beacon/mcp.go` | `1127-1243` | Заменить тело `runMCP` на код из раздела 2. Оставить `stateDir`, `openProcessStore`, `buildMCPDependencies(db, dir, nil)`, `stopVerdictSource`, `serveMCP`. |
| `cmd/beacon/mcp.go` | `436` | Удалить `mcp.AttachWake(s, newMCPWakeHandler(deps))`. |
| `cmd/beacon/mcp.go` | `440-452` | Удалить `newMCPWakeHandler`, если строка 436 удалена. |
| `cmd/beacon/opencode.go` | `128` | Удалить `SetServerEnsurer(serverEnsurerFunc(opencode.EnsureServer))`. Это убирает spawn для всей бинарной программы. |
| `cmd/beacon/mcp.go` | `546-590` | Заполнить `TaskClaimStore` адаптером `mcp.FileTaskClaimStore`. Иначе девятый инструмент остаётся нерабочим. |
| `cmd/beacon/main.go` | `121` | Необязательно. Пропустить `wireOpenCodePorts()` для команды `mcp`, чтобы процесс не строил HTTP-клиент. |
| `cmd/beacon/mcp.go` | `432`, `400-425`, `203-229`, `367-389` | Необязательно. Заменить `newMCPWaitResult` на `mcp.NewWaitResultServiceFromDeps(deps)` и удалить код precheck. Требует правки тестов. |
| `cmd/beacon/mcp.go` | `296-365`, `460-484`, `606-1009` | Необязательная физическая уборка после отключения вызовов. |
| `internal/infra/server/mcp.go` | `14`, `33-66` | Необязательная уборка HTTP-обработчика `/mcp`. В prod он не смонтирован. |
| `internal/infra/mcp/client.go` | `29-380` | Необязательная уборка remote-клиента после удаления ветки `BEACON_MCP_REMOTE`. |

Тесты, которые нужно пересмотреть при такой правке:

- `cmd/beacon/mcp_bs42_test.go:39-92` проверяет remote-ветку. После упрощения `runMCP` эти проверки теряют смысл.
- `cmd/beacon/mcp_test.go:125-129`, `246-262`, `346-348`, `372-392`, `423-455`, `503-514` проверяют `mcpConnector`, precheck и `serveMCP` в старой сборке.
- `cmd/beacon/mcp_test.go:525`, `709` используют `newMCPServer`. Они останутся рабочими, если убрать только `AttachWake`.
- `cmd/beacon/main_test.go:732-760` и `cmd/beacon/opencode_test.go:123-201`, `342-370` проверяют ensurer. Удаление строки `opencode.go:128` меняет ожидания этих тестов.
- `internal/infra/server/mcp_test.go:41`, `112`, `149`, `171` проверяют HTTP-обработчик. Его удаление потребует удалить файл или его часть.

## 6. Что не проверено

Прямого прогона после правки нет. Тесты и сборки по условию задачи не запускались. Влияние на CI не измерялось. Список строк составлен по чтению файлов на HEAD `2a3362f1`. Соседние отчёты `docs/research/mcp-q1-startup.md` и `docs/research/mcp-spawn-rootcause.md` описывают тот же стартовый путь и не противоречат этому документу.

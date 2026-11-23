# Вопрос 1: что происходит при запуске `beacon mcp`

Отчёт подготовлен 2026-11-21. Код прочитан на коммите `2a3362f1` в `/mnt/projects/beacon-handoff/beacon-impl`. Режим только чтение. `tools.beacon.*` не вызывались, `go test`, `go build` и `beacon gates` не запускались. Поиск шёл через `rg` с явными каталогами.

## Короткий ответ

`newOpencodePort()` в `cmd/beacon/mcp.go:1517` вызывается при каждом старте `beacon mcp`, ещё до первого `tools/call`. Вызов привязан к стартовому reconcile-циклу, а не к конкретному инструменту. `beacon_list_running` не добавляет вызова `newOpencodePort()`.

Фактический spawn `opencode serve` решается внутри `opencode.EnsureServer`. Живой сервер или `service.json` переиспользуются, новый процесс поднимается только когда обоих нет.

## 1. Точка входа

- `cmd/beacon/main.go:116-125`: `main()` вызывает `setupObserve`, затем `wireOpenCodePorts()`, затем `newDefaultCLI().Run(os.Args[1:])`, затем `cleanupObserve` и `os.Exit(code)`.
- `cmd/beacon/main.go:131-137`: `wireOpenCodePorts()` вызывает `newOpencodePort()` и `installOpenCodePorts(port)`. Это первый вызов `newOpencodePort()` в процессе.
- `cmd/beacon/main.go:252-287`: таблица `runnerFor`. Запись `"mcp"` в строке 263 вызывает `mcpRunner(c.errOut)`.
- `cmd/beacon/mcp.go:392-398`: `mcpRunner` вызывает `runMCP(context.Background(), newDefaultCLI())`.
- `cmd/beacon/mcp.go:1127`: начало `runMCP`.

Порядок внутри `runMCP`:

1. `mcp.go:1137-1144`: `stateDir()` и `readMCPConnection(dir)`.
2. `mcp.go:1152-1158`: `mcpPrecheck`.
3. `mcp.go:1160-1163`: `mcpConnector`, сохранение `activeMCPConnection`.
4. `mcp.go:1169-1178`: если включён `BEACON_MCP_REMOTE`, функция возвращается через `serveMCPRemote`. Ветка не открывает store, не запускает reconcile-цикл и не создаёт wake-порты. Это единственный стартовый путь без вызова `reconcileWaker`.
5. `mcp.go:1183-1203`: `openProcessStore`, `buildMCPDependencies`, сборка `reconcileConfig`.
6. `mcp.go:1214-1217`: `cfg.onPollerOwner` получает функцию, которая вызывает `startHTTPAPI` и `startWebhookReceiver`.
7. `mcp.go:1220`: `runReconciler(runCtx, cfg)`.
8. `mcp.go:1242`: `serveMCP(ctx, deps)` запускает stdio-транспорт MCP (`mcp.go:456-458`).

Шаги 6 и 7 происходят до шага 8, то есть до того, как придёт любой `tools/call`.

## 2. Куда ведёт `mcp.go:1517`

Строка 1517 находится в функции `opencodeWakePorts()` (`mcp.go:1516-1542`). Её единственный вызов в нетестовых файлах находится в `reconcileWaker()` на `mcp.go:1549`.

`reconcileWaker()` (`mcp.go:1547-1556`) вызывается в четырёх местах.

| Строка | Функция | Когда выполняется |
| --- | --- | --- |
| 906 | `startHTTPAPI` | из `cfg.onPollerOwner` на старте |
| 1586 | `makeEscalator` | из `runLadderPass` в первом проходе reconcile-цикла |
| 1664 | `runLadderPass` | из `ReconcilePass` в первом проходе reconcile-цикла |
| 1732 | `ReconcilePass` | из `ReconcileLoop` в первом проходе reconcile-цикла |

Первый путь. `runReconciler` (`mcp.go:1787-1810`) берёт `acquirePollerLock` в строке 1788. Если lock получен, строка 1797 вызывает `cfg.onPollerOwner(ctx)`. Callback из `mcp.go:1214-1217` вызывает `startHTTPAPI` (`mcp.go:894-918`). В структуре `httpapi.Config` строка 906 содержит `reconcileWaker(cfg.db)`. Тот в строке 1549 вызывает `opencodeWakePorts()`, а тот в строке 1517 вызывает `newOpencodePort()`.

Второй путь. `runReconciler` в строке 1801 запускает горутину `ReconcileLoop`. `ReconcileLoop` (`mcp.go:1763-1783`) вызывает `ReconcilePass` сразу, в первой итерации цикла, ещё до ожидания интервала (1769). `ReconcilePass` (1723) вызывает `runLadderPass` (1725), тот вызывает `makeEscalator` (1674), а `makeEscalator` (1581-1590) вызывает `reconcileWaker` (1586). Дополнительно `ReconcilePass` обращается к `reconcileWaker` напрямую в строке 1732.

Оба пути работают без участия инструментов MCP. Ни один `tools/call` не нужен, чтобы дойти до `newOpencodePort()`.

`newOpencodePort()` (`cmd/beacon/opencode.go:118-129`) вызывает `resolveOpencodeConfig` (119), `opencode.New` (123) и `SetServerEnsurer(serverEnsurerFunc(opencode.EnsureServer))` (128). Строка 128 ставит ensurer для всех следующих вызовов. Первый вызов в процессе делает `main()` через `wireOpenCodePorts()` (`main.go:131-137`), поэтому к моменту старта reconcile-цикла ensurer уже установлен, если прочитан `service.json`.

## 3. Ответ на вопрос 3

Да. Старт `beacon mcp` сам вызывает путь создания `opencode serve`, до первого вызова инструмента.

Доказательство по строкам. `runMCP` в `mcp.go:1220` вызывает `runReconciler`. Тот в `mcp.go:1797` вызывает `onPollerOwner`. Callback в `mcp.go:1215` вызывает `startHTTPAPI`. Тот в `mcp.go:906` вызывает `reconcileWaker`. Тот в `mcp.go:1549` вызывает `opencodeWakePorts`. Тот в `mcp.go:1517` вызывает `newOpencodePort`. Далее `opencode.go:119` вызывает `resolveOpencodeConfig`, `opencode.go:103-105` вызывает `installedServerEnsurer.EnsureServer`, а `opencode.go:128` устанавливает ensurer в `opencode.EnsureServer`. Функция `EnsureServer` (`internal/infra/opencode/server.go:390-429`) в строке 410 вызывает `startServer`. Тот через `startServeProcess` (`server.go:103-128`) собирает команду `exec.Command("opencode", "serve", "--hostname", ..., "--port", ...)` в строках 104-105 и запускает её в строке 119. У дочернего процесса стоит `Pdeathsig: syscall.SIGTERM` (118).

Уточнение про фактический spawn. `EnsureServer` сначала переиспользует живой сервер Beacon (`server.go:394-396`), затем `service.json` (`server.go:397-399`) и только после этого создаёт новый процесс (`server.go:401-428`). Если `service.json` доступен, новый `opencode serve` не поднимается, а вызывающий код получает адрес из файла. В текущей установке такой файл есть, что зафиксировано в `docs/research/process-leak-mechanism.md:54`. Вызов `newOpencodePort()` при старте при этом всё равно происходит.

Второй стартовый путь, `ReconcileLoop`, `ReconcilePass`, `makeEscalator`, `reconcileWaker`, ведёт к тому же `newOpencodePort()`. Поэтому даже без `onPollerOwner` вызов остаётся в стартовом коде.

Исключения.

- `BEACON_MCP_REMOTE` (`mcp.go:1169-1178`) возвращает управление до `runReconciler`. Стартового вызова через wake-порты нет.
- `runReconciler` вызывает `onPollerOwner` и запускает `ReconcileLoop` только когда процесс выиграл `beacon-poller.lock` (`mcp.go:1788-1804`). Второй процесс с тем же state directory не вызывает `reconcileWaker` на старте.

## 4. Ответ на вопрос 4

Нет. `tools.beacon.beacon_list_running` не создаёт `opencode serve` и не вызывает `newOpencodePort()`.

Инструмент зарегистрирован в `internal/infra/mcp/server.go:166-171` и `680-694`. Обработчик `ListRunningHandler.Handle` живёт в `internal/infra/mcp/tool_list_running.go:560` и читает `StateSnapshot` через `SnapshotReader`. Заголовок файла в `tool_list_running.go:1` прямо называет инструмент чистым чтением состояния. Поиск по каталогу `internal/infra/mcp` не нашёл ни `newOpencodePort`, ни `EnsureServer`. Пакет `internal/infra/mcp` не импортирует `cmd/beacon`, поэтому вызвать функцию из `package main` он не может.

Третий нетестовый вызов `newOpencodePort()` лежит в `cmd/beacon/registry_sync.go:294-296` внутри `registrySyncOpenSource`. Он относится к подкоманде `registry sync` и не участвует ни в старте `beacon mcp`, ни в `beacon_list_running`.

Итог. Если старт уже создал `serve`, это случилось на шаге `runReconciler`, а не в инструменте. Если старт не создал, вызов `beacon_list_running` ничего не изменит.

## Что не проверено

Прямой трассировки от конкретного PID `opencode serve` к конкретному старту `beacon mcp` в этом отчёте нет. Вывод опирается на код `main.go`, `mcp.go`, `opencode.go` и `server.go`. Наличие `service.json` и список живых процессов новыми командами не проверялись, а взяты из `docs/research/process-leak-mechanism.md:54`.

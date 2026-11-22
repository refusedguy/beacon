# Исследование: P1 6 и 7 ТЗ, контракт MCP и порты наблюдаемости и реестра

Дата: 2026-11-22. База исследования: `origin/main` `f25294ba`. Проверка перед PR: `aa8be804`. Между ними вошли только docs-коммиты `#1484`..`#1487`, исходные файлы из этого отчёта не менялись. Режим: чтение. Тесты, сборка, гейты, `go test`, `go build` и `batch-apply` не запускались. Источники: чтение файлов, `git log`, `git show` и `grep` по каталогам `cmd/beacon/`, `internal/infra/mcp/`, `internal/infra/cibridge/`, `internal/app/orchestrator/`, `internal/infra/taskstore/`, `AGENTS.md` и `docs/tasks/`.

Приоритет из ТЗ: P1.6 это рассинхрон контракта 7/9/10 (AUD-09), P1.7 это описание инструментов (AUD-10). Оба пункта влиты. Осталась третья часть AUD-04, которую PR #1470 не тронул: подключение портов наблюдаемости и реестра в composition root.

## 1. Что уже влито, не дублировать

| Карточка | PR | file:line и символ | Факт |
| --- | --- | --- | --- |
| AUD-09 (P1.6) | #1466, коммит `0482830c` | `internal/infra/mcp/client.go:290-308` `remoteToolDescriptions`, `:310-320` `remoteInputSchemaFactories` | 10 описаний и 10 фабрик схем. Было 7. |
| AUD-10 (P1.7) | #1467, коммит `52be6898` | `AGENTS.md:115` «Десять инструментов», `internal/infra/mcp/server.go:93-114` `CycleInstructions`, `:105` «Ten tools» | Документация и handshake говорят про 10. |
| PROTO-03 | #1481, коммит `8608917d` | `internal/infra/server/mcp.go` обработчик `/mcp` | Аутентифицированный `/mcp` смонтирован. |
| AUD-04 часть 1 | #1470, коммит `d26bd3ba` | `cmd/beacon/mcp.go:700-708` `openProcessStore`, `:707` `store.OpenProduction` | Переход на OpenProduction сделан. |
| PROTO-08 | #1449, коммит `daa93aa5` | `cmd/beacon/registry_list.go:73-79` `registryListSource`, `:101` `openProjectRegistryListReader` | Source по умолчанию строит реальный scoped reader. |
| PROTO-07 | #1457, коммит `058f9430` | `internal/infra/mcp/tool_stats.go:565-628` `GraphReadySignal`, `ReadGraphReady`, `StatsOutput.GraphReady` | Типизированный сигнал в stats есть. |
| INTG-02 | #1456, коммит `7de845ba` | `internal/app/orchestrator/claim.go:115-119` `NewClaimGenerationWithStore` | Дurable ledger есть. |
| BCX-21, ARDH-10 | #1411, #1165 | `cmd/beacon/catalog.go:65-77` `commands()`, `:72` `runnerFor[c.Name]` | Catalog и runnerFor синхронны. |

Статусы карточек AUD-04, AUD-09, AUD-10, PROTO-07, PROTO-08 и INTG-02 в `docs/tasks/` остались `todo`. Код влит, статус не обновлён. Это отдельная работа по синхронизации карточек, не дубль кода.

## 2. Что осталось неподключённым

| ID | Приоритет | Проблема | file:line и символ | Решение |
| --- | --- | --- | --- | --- |
| MP-01 | P1 | Порты наблюдаемости и generation не заданы в composition root | `cmd/beacon/mcp.go:710-779` `buildMCPDependencies`: нет вызовов сеттеров; `:513-521` `newMCPServer` | Добавить три порта в `mcp.Dependencies`, применить сеттеры в дескрипторах `server.go`, собрать и связать ledger в `buildMCPDependencies` и в `ReconcilePass`. |
| MP-02 | P1 | Нет production-реализации `GraphIndexReader` | `internal/infra/mcp/tool_stats.go:173-175` интерфейс; `internal/infra/taskstore/index.go:62-69` `Index`, `:202` `RebuildDir`, `:256` `Counts` | Добавить в `taskstore` адаптер с методом `GraphStatus(ctx) (domain.GraphStatus, error)`. Тип Satisfies порт структурно, без импорта `mcp`. |
| MP-03 | P1 | `graphReady` отсутствует в `TaskClaimOutput` | `internal/infra/mcp/tool_task_claim.go:48-59` `TaskClaimOutput`: 10 полей, нет `graphReady`; `AGENTS.md:115` обещает маркер | Добавить поле в `TaskClaimOutput`, заполнять из `GraphIndexReader` в обработчике claim. Обновить `docs/templates/executor-task.md:33`. |
| MP-04 | P2 | Статусы влитых карточек не совпадают с main | `docs/tasks/AUD-04.md:2`, `AUD-09.md:2`, `AUD-10.md:2`, `PROTO-07.md:2`, `PROTO-08.md:2`, `INTG-02.md:2`: везде `todo` | Обновить статусы и ссылки на PR после того, как MP-01 и MP-03 закроют часть 3 AUD-04. |

`registryListSource` и `catalog` в таблицу problems не попали. Проверка показала, что оба уже рабочие, детали в разделе 4.

## 3. Доказательства по каждому порту

### 3.1 `SetGraphIndexReader`

Порт объявлен в `internal/infra/mcp/tool_stats.go:169-175` `GraphIndexReader`. Сеттер описан в `:281-286` `StatsHandler.SetGraphIndexReader`. Запись маркера идёт в `:450-463`, строка `:461` добавляет `graphReady: <bool>` в `loadErrors`. Читатель `ReadGraphReady` живёт в `:598-616`.

Production не вызывает сеттер. Единственные вызовы: `internal/infra/mcp/tool_stats_graph_test.go:85` и `:134`. `grep` по `cmd/beacon/` не нашёл `SetGraphIndexReader` ни разу.

Причина в сборке. `internal/infra/mcp/server.go:713-727` `attachStats` создаёт `NewStatsHandler(reader)` только с `SnapshotReader`. `mcp.Dependencies` в `server.go:401-482` не имеет поля `GraphIndexReader`. Значит `StatsHandler.graph` в `:264` всегда nil, `:454` пропускает чтение, и `ReadGraphReady` возвращает `GraphReadyNoReader` по `tool_stats.go:573` и `:608`.

Вторая проблема: нет production-реализации порта. `GraphStatus(ctx)` реализуют только тестовые заглушки в `tool_stats_graph_test.go:19`. `domain.GraphStatus` описан в `internal/domain/types.go:1115-1128`, метод `Ready` в `:1127`. Индекс строится в `cmd/beacon/index.go:54-61` через `taskstore.OpenIndex` и `RebuildDir`. Адаптера от индекса к `domain.GraphStatus` нет.

### 3.2 `SetRegistryStatsReader`

Порт объявлен в `internal/infra/mcp/tool_stats.go:161-167` `RegistryStatsReader`. Сеттер описан в `:275-279` `StatsHandler.SetRegistryStatsReader`. Адаптер уже есть: `:177-202` `SnapshotRegistryStats` и `NewSnapshotRegistryStats`, который читает `snap.RegisteredAgents()`, `snap.StaleAgents()` и `snap.RegistryConfig()`.

Production не вызывает сеттер. Все шесть вызовов лежат в `internal/infra/mcp/tool_stats_test.go:597, 644, 663, 681, 682, 703`. `Snapshot` в `cmd/beacon/mcp.go:755-777` уже заполнен, поэтому адаптер можно связать без нового чтения базы. Опять мешает отсутствие поля в `Dependencies` и то, что `attachStats` создаёт обработчик внутри `server.go`.

Следствие: `beacon_stats` всегда отдаёт нулевые счётчики реестра, хотя адаптер и данные есть.

### 3.3 `graphReady` в `TaskClaimOutput`

Структура в `internal/infra/mcp/tool_task_claim.go:48-59` содержит `CardID`, `Title`, `Status`, `AgentID`, `Goal`, `Dependencies`, `OwnerFiles`, `PlanMode`, `ClaimedAt` и `Handoff`. Поля `graphReady` нет. `grep` по `internal/infra/mcp/tool_task_claim.go` и `cmd/beacon/*.go` не нашёл `graphReady`.

AGENTS.md обещает маркер в ответе claim: `AGENTS.md:115` и `docs/templates/executor-task.md:33`. Реально маркер есть только в stats, что зафиксировано в `docs/tasks/IDX-09.md:13`. IDX-09 влит как PR #908 и реализовал только stats. PROTO-07 добавил типизированный сигнал тоже только в stats.

Итог: агент, который читал claim, не видит состояния графа, хотя документация говорит обратное.

### 3.4 Generation ledger, `SetGenerationStore` и `WithGenerationStore`

Дurable реализация влита: `internal/app/orchestrator/claim.go:115-119` `NewClaimGenerationWithStore`, порт сохранения в `:63-67` `GenerationStore`. Это INTG-02, PR #1456.

Порты есть во всех трёх точках:
- `internal/infra/mcp/tool_claim.go:61-74` `GenerationLedger`, сеттер в `:393-401` `ClaimHandler.SetGenerationStore`, геттер в `:403-411`.
- `internal/infra/mcp/tool_ack.go:468-474` `AckHandler.SetGenerationStore`.
- `internal/infra/cibridge/bridge.go:152-154` `GenerationControl`, сеттер в `:377-387` `Bridge.WithGenerationStore`.

Вызовов в production нет. `grep` по `cmd/` не нашёл `NewClaimGeneration`, `NewBridge`, `WithGenerationStore` или `GenerationStore`. Bridge создаётся в `cmd/beacon/mcp.go:1944` `cibridge.New`, цепочка `:1945-1956` включает девять методов `With*`, но не `WithGenerationStore`. Дескриптор ack в `internal/infra/mcp/server.go:187-199` создаёт `NewAckHandler` и зовёт `SetBlockerControl` и `SetContinuationRegistry`, но не `SetGenerationStore`.

Следствие: поворот генерации из T3.457 не работает в production. `blocked:resolved` не делает старый claim недействительным, merge и unblock в bridge не меняют генерацию. Порты и реализация есть, соединения нет.

### 3.5 `registryListSource`, уже исправлено

`cmd/beacon/registry_list.go:73-79` задаёт `registryListSource` со значением по умолчанию `openProjectRegistryListReader(root)`. Реализация в `:101-129` открывает store из `stateDir`, строит `projectStateKey` и `store.NewScopedRegistry`. Команда вызывает source в `:203`. PR #1449, PROTO-08. Старый текст AUD-04 про « registry reader not connected » устарел.

### 3.6 `catalog` против `runnerFor`, уже исправлено

`cmd/beacon/catalog.go:32-63` содержит 30 записей. Функция `:65-77` `commands()` читает `runnerFor[c.Name]` из `cmd/beacon/main.go:249-252` на каждом вызове. Комментарий в `catalog.go:65-68` прямо говорит, что метаданные и таблица раннеров не могут разойтись. PR #1411 и #1165. Текст AUD-04 про «10 отсутствующих команд» устарел.

## 4. Счётчик

- Проверено портов и точек сборки: 6 (граф, реестр, graphReady в claim, generation в claim, generation в ack, generation в bridge).
- Реально неподключено: 4 точки (подключение `GraphIndexReader`, подключение `RegistryStatsReader`, поле `graphReady` в `TaskClaimOutput`, связь `GenerationLedger` с claim, ack и bridge). Три из них ведут в MP-01, MP-02 и MP-03. Четвёртая входит в MP-01.
- Уже влито и не дублируется: 8 PR из раздела 1.
- Ложных срабатываний в старом тексте AUD-04: 2 (`registryListSource`, `catalog`).
- Карточек в батче: 4 (MP-01..MP-04), ADR 0314, один eval, шесть фактов.

## 5. Ограничение OwnerFiles

Карточки MP-01, MP-02 и MP-03 в фазе P1 не пересекаются по файлам.

- MP-01: `internal/infra/mcp/server.go`, `cmd/beacon/mcp.go`, `cmd/beacon/mcp_test.go`.
- MP-02: `internal/infra/taskstore/index.go`, `internal/infra/taskstore/index_test.go`.
- MP-03: `internal/infra/mcp/tool_task_claim.go`, `internal/infra/mcp/tool_task_claim_test.go`, `docs/templates/executor-task.md`.
- Generation ledger входит в MP-01 как часть сборки. Отдельной карточки с теми же файлами нет, чтобы не пересекаться в фазе.
- MP-04: фаза P2, только файлы карточек.

MP-01 зависит от MP-02, потому что в `cmd/beacon/mcp.go` нечего связывать, пока `taskstore` не отдаст `domain.GraphStatus`.

### Конфликт OwnerFiles с другими батчами

`cmd/beacon/mcp.go` уже занят в трёх принятых research-батчах. Это не конфликт внутри моего батча, но оркестратору нужно учесть очередь.

- `docs/research/research-dir4-protocols-batch.json`, PR4-01, фаза P1: `cmd/beacon/mcp.go`, `cmd/beacon/spawn.go`.
- `docs/research/research-dir5-integrity-batch.json`, IN5-01 P0, IN5-02 P0, IN5-04 P1, IN5-05 P1, IN5-06 P1: все включают `cmd/beacon/mcp.go`.
- `docs/research/research-dir4-protocols-batch.json`, PR4-03, фаза P2: `cmd/beacon/inspect.go`. Это потребитель типизированного сигнала `graphReady`, а не подключение reader. С MP-01 не совпадает.

MP-01 поэтому должен идти после IN5 и PR4-01 либо быть слиян с одной из этих карточек. MP-02 и MP-03 свободны от этого конфликта и могут идти первыми.

## 6. Что не проверялось

Тесты, сборка, гейты, `beacon index build`, CLI Beacon, `batch-apply` и ручной merge не запускались. Номера строк сняты чтением файлов на базе `f25294ba`. Все утверждения про «вызовов нет» основаны на `grep` по каталогам из первого абзаца, а не на анализе всех возможных рефлексивных вызовов.

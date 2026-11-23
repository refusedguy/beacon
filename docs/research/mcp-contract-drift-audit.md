# Аудит дрейфа MCP-контракта

Дата: 2026-11-21. Режим только чтение. Код, карточки и конфиги не менялись. Тесты, сборки и гейты не запускались.

## Методика и источники

Основной ствол проверен на коммите `d3b1bfb1` (`feat(gate): offline tools/list contract check (RS-MCP-02) (#843)`). Дополнительно прочитаны worktrees репозитория, где живут попытки расширить контракт до девяти инструментов. Вхождения в `worktrees/` не считаются рабочим кодом main, но фиксируются как отдельный слой дрейфа.

Первичные источники:

| Что | Путь |
| --- | --- |
| Реестр имён и аннотаций | `internal/infra/mcp/server.go:103-166`, `467-526` |
| Фабрики выходных схем | `internal/infra/mcp/schema.go:293-355` |
| Золотой `tools/list` | `internal/infra/mcp/contract_test.go:19-50`, `271-310` |
| Офлайн-гейт описаний | `tools/beacon-gates/gate/tool_contract.go`, `tool_contract_test.go`, `tools/beacon-gates/main.go:24-29` |
| Контракт ADR | `docs/adrs/0094-seven-tools.md`, `0263-mcp-boundary.md`, `docs/adrs/0268-task-claim-tool.md` (только worktrees) |
| Старые контракты | `docs/adrs/0155-blocked-watch.md`, `0156-orchestrator-workload.md`, `0186-mcp.md` |
| Eval-контракт | `evals/main.go:40-67`, `227-244` |
| Девятый инструмент | `worktrees/ak1/internal/infra/mcp/tool_task_claim.go`, `worktrees/ak1/docs/adrs/0268-task-claim-tool.md` |
| Opt-in чтение потока | `worktrees/ak1/internal/infra/mcp/tool_thread_read.go`, `worktrees/ak1/internal/infra/mcp/schema.go:242-273`, `server.go:742-768` |
| Незарегистрированные обработчики | `internal/infra/mcp/tool_claim.go:61-65`, `tool_wake.go:105-107`, `728`, `1740-1747` |
| Список отложенных символов | `tools/beacon-gates/gate/config/reachability.json:75-120` |
| Подтверждённый мёртвый код | `docs/research/deadcode-audit.md:30-34` |
| Спецификация t3_thread_read | `docs/tasks/T3.236.md`, `docs/research/output-limits-audit.md:159` |

Термин "slim" в репозитории не встречается. В таблицах ниже под slim понимается документированный минимальный контракт: восемь имён ADR 0094 и семь имён в `evals/main.go`, ADR 0263 и старых ADR.

## 1. Карта инструментов

### Инструменты main (8 зарегистрированных)

| Инструмент | Регистрация | Схема вывода | Обработчик | Golden-тесты | ADR |
| --- | --- | --- | --- | --- | --- |
| `beacon_watch_pr` | `ToolNames` + `toolAnnotations`, `AttachWatchPR` (`server.go:511`) | `OutputSchema[WatchPROutput]` (`schema.go:296`) | `tool_watch_pr.go`, `NewWatchPRHandler` | `contract_test.go:129-215`, `271-310`, `421-545`; `server_test.go:57-89`; `tool_watch_pr_test.go` | 0094, 0088 (executor), 0036 |
| `beacon_wait_result` | `ToolNames`, `AttachWaitResult` (`server.go:512`) | `OutputSchema[WaitResultOutput]` (`schema.go:297`) | `tool_wait_result.go`, `WaitResultService` | `contract_test.go:474-483`, `contract_test.go:271-310`; `tool_wait_result_test.go` | 0094, 0036, 0037, 0040 |
| `beacon_get_result` | `ToolNames`, `AttachGetResult` (`server.go:513-515`) | `OutputSchema[GetResultOutput]` (`schema.go:298`) | `tool_get_result.go`, `NewGetResultHandler` | `contract_test.go:487-502`, `526-544`; `tool_get_result_test.go` | 0094, 0031, 0009, W11-B, T5.36 |
| `beacon_list_running` | `ToolNames`, `attachListRunning` (`server.go:516`, `537-552`) | `listRunningOutputSchema`, ручное ослабление null-карт (`schema.go:325-341`) | `tool_list_running.go`, `NewListRunningHandler` | `contract_test.go:504-517`; `tool_list_running_test.go` | 0094, 0025 |
| `beacon_ack` | `ToolNames`, `AttachAck` (`server.go:517-522`) | `OutputSchema[OKOutput]` (`schema.go:300`) | `tool_ack.go`, `NewAckHandler` | `contract_test.go:519`; `tool_ack_test.go` | 0094, 0155, 0072 |
| `beacon_cancel_watch` | `ToolNames`, `RegisterCancelWatch` (`server.go:523`) | `OutputSchema[OKOutput]` (`schema.go:301`) | `tool_cancel_watch.go`, `NewCancelWatcher` | `contract_test.go:520`; `tool_cancel_watch_test.go` | 0094 |
| `beacon_stats` | `ToolNames`, `attachStats` (`server.go:524`, `558-573`) | `OutputSchema[StatsOutput]` (`schema.go:302`) | `tool_stats.go`, `NewStatsHandler` | `contract_test.go:521-524`; `tool_stats_test.go` | 0094, T5.60 |
| `beacon_defer_wiring` | `ToolNames`, `AttachDeferWiring` (`server.go:525`) | `OutputSchema[DeferWiringOutput]` (`schema.go:303`) | `tool_defer_wiring.go`, `NewDeferWiringHandler` | `contract_test.go:129-215`, `271-310`; `tool_defer_wiring_test.go` | 0094, W12-B |

Все восемь схем выводятся из Go-типов. Ручная правка есть только в `listRunningOutputSchema`: она расширяет два поля до `null | object`, потому что обработчик отдаёт nil-карты.

### Инструменты вне контракта main

| Инструмент | Где живёт | Регистрация | Схема | Golden-тесты | ADR |
| --- | --- | --- | --- | --- | --- |
| `beacon_task_claim` | `tool_task_claim.go` в worktrees `z1`, `ak1` и других | В main нет. В 9-инструментальных worktrees зарегистрирован через `toolDescriptors` (`ak1/server.go:215-223`) | `taskClaimOutputSchema` (`ak1/schema.go:382-384`), поля из ADR 0268 | В worktrees `expectedToolNames` выводятся из дескрипторов. Золотой 18212 / SHA `ba0a9f...` | ADR 0268 только в worktrees. В main ADR нет |
| `t3_thread_read` | `tool_thread_read.go` в worktrees | Нет. `AttachThreadRead` определён в `ak1/server.go:748`, нигде не вызывается. `server.go:131` прямо держит его вне `toolDescriptors` | `ThreadReadOutputSchema` (`ak1/schema.go:268-273`) вне реестра фабрик | Тест обработчика есть, золотой листинг его не содержит | ADR нет. Требование живёт в `T3.236.md` |
| `beacon_claim`, `beacon_release` | `tool_claim.go:61-65` в main | Нет. Комментарий называет имена "deliberately absent from ToolNames". `AttachClaim` не вызывается | Локальные `ClaimOutput`, `ReleaseOutput`, `RecoveryOutput` в файле обработчика | Нет золотого теста листинга | ADR 0263 упоминает claim как причину не расширять контракт |
| `beacon_lease_worktree`, `beacon_write_worktree`, `beacon_release_worktree`, `beacon_heartbeat_worktree`, `beacon_extend_holder_lease` | `tool_claim.go:484-487`, `933` в main | Нет. `AttachWorktreeLease` (`878`) и `AttachHolderLease` (`1208`) определены, не вызываются | Локальные типы в `tool_claim.go`, фабрик в `schema.go` нет | Нет золотого теста листинга | ADR 0037, 0038, 0044, 0046 описывают механику лизов, но не регистрируют инструменты |
| `beacon_wake` | `tool_wake.go:105-107`, `1740-1747` в main | Нет. `AttachWake` определён, не вызывается. `deadcode-audit.md:30` фиксирует 25 вызовов только в тестах | Схема вывода не зарегистрирована | Нет | ADR 0263 явно запрещает тихую регистрацию wake |

## 2. Расхождения

### 2.1 Инструмент есть в контракте одного слоя, но отсутствует в другом

1. `evals/main.go:43` держит `contractToolCount = 7`, а `contractTools` перечисляет семь имён без `beacon_defer_wiring`. Eval 02 сравнивает отсортированные имена через `slices.Equal` (`evals/main.go:242-244`). Сервер main отдаёт восемь. Проверка 02 на main красная. RS-MCP-01 зафиксировал это расхождение в `docs/tasks/RS-MCP-01.md:11`, но не устранил его.
2. ADR 0263 в решении называет "ровно семь имён" (`docs/adrs/0263-mcp-boundary.md:11`), хотя его же контекст (` строка 7`) описывает текущий тест с 16 KiB и восемью инструментами. ADR 0094 accepted фиксирует восемь. ADR 0155, 0156 и 0186 всё ещё пишут "семь инструментов" и бюджет 64 KiB.
3. ADR 0268 добавляет девятый инструмент и поднимает потолок до 20 KiB только в worktrees. На main этого ADR нет, поэтому перенос девятого имени на main сейчас не имеет акта решения.

### 2.2 Обработчик и схема есть, регистрации нет

1. `beacon_claim` и `beacon_release`: имена определены, обработчики есть, в `ToolNames` не входят. ADR 0263 считает это нормальным состоянием.
2. `beacon_wake` и `AttachWake`: то же состояние. `deadcode-audit.md:30` называет риск medium и указывает, что удаление потянет 25 вызовов в тестах.
3. Пять lease-инструментов из `tool_claim.go` имеют готовые `Attach`-хелперы, но не входят в реестр. `docs/research/go-metaprogramming.md:79` прямо называет риск: автоматический `AddTool` по всем файлам с обработчиками зарегистрировал бы claim, wake и четыре lease-инструмента без решения владельца. В main таких имён семь, включая claim и release.
4. `NewCapabilityFallbackWakeHandler` и ещё четыре wake-конструктора вынесены в `REACH-UNWIRED` (`reachability.json:75-120`). Причина в конфиге обещает подключение в Waves 6-8, что противоречит принятому ADR 0263. Эту же коллизию фиксирует `deadcode-audit.md:31`.
5. `t3_thread_read` в 9-инструментальных worktrees имеет input, output, схему, обработчик и `AttachThreadRead`, но намеренно оставлен вне реестра. В `server.go:131` написано, что десятое имя сломало бы byte-pinned листинг. Результат: код готов, пользователь MCP его не видит, ADR на него нет.

### 2.3 Поля вывода, которых нет в документированном контракте

ADR 0094 перечисляет только исходные поля. Схемы выводятся из структур, поэтому добавки попадают в `tools/list` автоматически. Контрактный тест намеренно открывает `additionalProperties` через `relaxSchema` (`contract_test.go:351-415`) и прямо позволяет новым полям проходить без объявления. Комментарий в `contract_test.go:358-360` называет `failureExcerpt` примером такого поля. Forward-compatibility зонд в `contract_test.go:526-544` добавляет `failureExcerpt`, `nextAction`, `nextCommand` и `nextHint` в проверяемый ответ, хотя объявляет только базу.

Не задокументированные добавки на main:

| Инструмент | Поле | Источник |
| --- | --- | --- |
| `beacon_watch_pr` | `executor` с шестью подполями | T5.32, `schema.go:31-50` |
| `beacon_wait_result` | `reason` | `WaitReasonOutput` в `tool_wait_result.go:148-185` |
| `beacon_get_result` | `resultType`, `taskId`, `jobID`, `taskStatus`, `pollIntervalMs`, `instructions`, `nextAction`, `nextCommand`, `nextHint`, `failureExcerpt`, `configId` | `schema.go:115-138`, ADR 0031, W11-B, T5.36 |
| `beacon_list_running` | `blockerID`, `blocked`, `phases` в `WatchEvent` | `schema.go:219-226` |
| `beacon_stats` | `registeredAgents`, `staleAgents`, `configId` | T5.60, `schema.go:279-284` |

Отдельный случай `beacon_wait_result`: фабрика схемы использует `WaitResultOutput` (`schema.go:297`), а обработчик отдаёт `WaitReasonOutput` с полем `reason` (`tool_wait_result.go:154-185`). Advertised OutputSchema не описывает `reason`. Тест это проглатывает, потому что `relaxSchema` открывает структуру. Клиент, который проверяет ответ по схеме строго, получит расхождение между объявлением и фактом.

В 9-инструментальной ветке добавляются `StatsOutput.Blocked` (T3.440, `ak1/schema.go:310-312`) и `Watch.Depth` (T3.450). `Depth` попал в схему `beacon_list_running` через `[]domain.Watch` без правки `schema.go`.

`docs/api/openapi.yaml` не содержит имён MCP-инструментов. HTTP-контракт и MCP-контракт не связаны общей проверкой.

### 2.4 Офлайн-гейт из HEAD не подключён

HEAD `d3b1bfb1` добавил `CheckToolContract` и девять тестов к нему. `tools/beacon-gates/main.go:24-29` регистрирует только четыре подкоманды: capabilities, layers-ast, layers-list, reachability. Подкоманды tool-contract нет. В `.github`, `scripts`, `docs/gates` и `docs/runbooks` упоминаний `TOOL-DESC`, `CheckToolContract` и `tool-contract` нет. Бинарник `beacon-gates` пересобран, но новая проверка не имеет точки входа. Фикстура в `tool_contract_test.go:17-57` синтетическая и содержит восемь имен, не реальный ответ tools/list.

## 3. Эволюция золотого `tools/list`

Золотой артефакт состоит из четырёх частей в `contract_test.go`: точный размер байт, SHA-256, фиксированный порядок имён и ожидаемое множество имён с классами аннотаций. `TestToolsListStabilityAcrossConnections` сравнивает две независимые сессии побайтово, поэтому порядок и любая правка описания ловятся тестом.

История значений в объектах базы git:

| Поколение | Байт | SHA-256 (префикс) | Коммит | Что изменилось |
| --- | --- | --- | --- | --- |
| main HEAD | 16095 | `8324205f...` | `95481c3b` (RS-MCP-01) | Восемь инструментов, первая фиксация золотого |
| промежуточное | 16153 | `bb50bd3b...` | до `669a8a50` | Ответ tools/list до правки срока жизни watchID |
| 1 | 16336 | `315a91fb...` | `669a8a50` (#882), правка описания в `21cd5a84` | watchID получил срок жизни 30 минут, описание `beacon_wait_result` выросло |
| 2 | 17922 | `cc1b166a...` | `1d10c03f` (#913, IDX-06) | Девятый инструмент `beacon_task_claim`, бюджет 16 KiB сменён на 20 KiB |
| 3 | 17961 | `dce2a53c...` | `843a4d4a` (#1120, T3.440) | `StatsOutput.Blocked`, +39 байт |
| 4 | 18212 | `ba0a9f42...` | `1fc573dd` (#1121, T3.450) | `Watch.Depth` в схеме `beacon_list_running`, +251 байт |

Распределение по 181 worktree: 32 дерева на 16336, 79 на 17922, 4 на 17961, 43 на 18212. Main остался на 16095. Девятое имя и ADR 0268 живут в worktrees, 128 деревьев содержат ADR 0268.

### Оценка процедуры обновления

Процедура в основном безопасна, с тремя оговорками.

Что работает. Во всех пяти переходах золотые константы обновлялись в том же коммите или PR, что и вызвавшее изменение. `1d10c03f` обновил и бюджет, и байт, и SHA, и список имён. `843a4d4a` и `1fc573dd` обновили байт и SHA в том же PR, где выросла схема. Тест краснеет до обновления золотого, поэтому случайная правка описания или схемы не проходит молча. Порядок и уникальность имён проверяются отдельно, а SHA ловит перестановку равных по длине имен.

Оговорка 1. Значения пишутся вручную по измерению текущего сервера. Автоматического генератора нет, и это осознанный выбор: `docs/research/go-metaprogramming.md:67` называет генерацию золотых значений превращением теста в эхо генератора. Риск в обратную сторону: если разработчик добавит инструмент и в том же PR перепишет байт и SHA по результату работы сервера, тест подтвердит факт, но не подтвердит решение владельца. ADR 0268 был бы таким подтверждением, но на main его нет.

Оговорка 2. В 9-инструментальных worktrees `expectedToolNames`, `toolsListOrder` и `expectedToolKinds` выводятся из `toolDescriptors` (`ak1/contract_test.go`), а байт и SHA остаются ручными. Добавление имени в дескриптор автоматически обновляет три проверки из четырёх. Независимая проверка сохраняется только в двух константах.

Оговорка 3. Домино-эффект из домена. `Watch.Depth` в `1fc573dd` изменил выходную схему через отражение `[]domain.Watch`, хотя коммит в MCP затронул только `contract_test.go`. Единственным предохранителем оказался ручной пересчёт золотого. Любое новое поле в `domain.Watch` или `domain.Check` потребует той же правки. Офлайн-гейт, который мог бы проверить качество описаний нового поля, в HEAD не подключён к CLI.

## 4. Итоговая таблица, топ-10

| Инструмент | Регистрация | Схема | Slim / расхождение | Риск |
| --- | --- | --- | --- | --- |
| `beacon_task_claim` | 9-е имя в worktrees через `toolDescriptors`, в main нет | `taskClaimOutputSchema`, 10 полей по ADR 0268 | 8 в ADR 0094, 7 в evals, 9 в worktrees. ADR 0268 только вне main | Высокий. Перенос на main без ADR и без atomic golden ломает контракт |
| `t3_thread_read` | `AttachThreadRead` определено, не вызывается | `ThreadReadOutputSchema` вне реестра фабрик | Требование в T3.236, ADR нет, 10-е имя запрещено комментарием в server.go | Высокий. Готовый код не доступен клиенту, спецификация ~30000 символов не закрыта |
| `beacon_wait_result` | В `ToolNames` main | Фабрика от `WaitResultOutput`, обработчик отдаёт `WaitReasonOutput` | Поле `reason` отсутствует в advertised схеме, разрешено `relaxSchema` | Средний. Строгий клиент отвергнет ответ или не увидит поле |
| `beacon_stats` | В `ToolNames` main | `OutputSchema[StatsOutput]`, 7 исходных полей | `registeredAgents`, `staleAgents`, `configId` сверх ADR 0094; `blocked` в 9-ветке | Средний. Рост схемы меняет golden без правки ADR |
| `beacon_list_running` | В `ToolNames` main, `attachListRunning` | `listRunningOutputSchema` с ручной правкой null-карт | `blockerID`, `blocked`, `phases` сверх ADR; `Watch.Depth` прилетел из домена | Средний. Изменение домена меняет MCP-контракт |
| `beacon_get_result` | В `ToolNames` main | `OutputSchema[GetResultOutput]`, 11 добавочных полей | Большинство полей не перечислены в ADR 0094; forward-compat зонд добавляет поля без объявления | Средний. Документация отстаёт от схемы |
| `beacon_watch_pr` | В `ToolNames` main | `OutputSchema[WatchPROutput]` | `executor` с шестью подполями из T5.32 не в ADR 0094 | Низкий. Поле необязательное и обратно совместимо |
| `beacon_claim` / `beacon_release` + 5 lease-имен | Имена и `Attach`-хелперы в `tool_claim.go`, вне `ToolNames` | Локальные типы обработчика, фабрик в `schema.go` нет | ADR 0263 фиксирует отказ для claim, но 7 имён и тесты остаются | Средний. Готовый код и риск случайной регистрации авто-`AddTool` |
| `beacon_wake` | `AttachWake` не вызывается | Нет зарегистрированной схемы | `deadcode-audit.md:30`, 5 конструкторов в REACH-UNWIRED с устаревшей причиной | Средний. Конфликт конфига и ADR 0263 |
| `beacon_defer_wiring` | В `ToolNames` main | `OutputSchema[DeferWiringOutput]` | Единственное расхождение: evals считает 7 имён и не знает восьмое | Средний. Eval 02 красный на main |

## 5. Главные находки

1. **`beacon_task_claim` готов в 43 worktrees и отсутствует в main.** Девятый инструмент зарегистрирован через `toolDescriptors`, имеет ADR 0268, схему и золотой 18212 / `ba0a9f...`. Main остался на восьми инструментах и 16095. Два контракта одновременно существуют в одном хранилище объектов git.

2. **Офлайн-гейт из HEAD не вызывается.** `CheckToolContract` добавлен с девятью тестами и новым бинарником, но не попал в `registry` из четырёх подкоманд и нигде не вызван из CI. Проверка качества описаний tools/list сейчас не работает.

3. **Eval-контракт отстал на один инструмент.** `evals/main.go` ожидает семь имён, сервер отдаёт восемь. Расхождение зафиксировано в RS-MCP-01 и до сих пор не устранено.

4. **Вне реестра лежат девять готовых имён и пять wake-конструкторов.** В main это `beacon_claim`, `beacon_release`, пять lease-инструментов и `beacon_wake`. В worktrees добавляется `t3_thread_read` с `AttachThreadRead`. У всех есть код и тесты, но нет записи в `ToolNames`. Причины в `REACH-UNWIRED` противоречат ADR 0263, а авто-`AddTool` по файлам с обработчиками зарегистрировал бы их без решения владельца.

5. **Процедура обновления золотого выдерживает проверку во времени, но не проверяет намерение.** Все переходы 16095 → 16336 → 17922 → 17961 → 18212 обновляли константы в том же PR. При этом значения ручные, а `relaxSchema` пропускает новые поля без объявления. Поле `Watch.Depth` изменило схему из домена, и только ручной пересчёт SHA поймал это изменение.

# Аудит метрик и наблюдаемости

## Способ проверки

Отчёт составлен по исходному коду на коммите `d3b1bfb1`. Тесты не запускались: AGENTS.md запрещает локальный `go test ./...`, проверку проведёт CI на PR. Все выводы ниже получены чтением кода и поиском по явным каталогам.

Каталоги поиска: `internal/infra/observe`, `internal/infra/server`, `internal/infra/wake`, `internal/infra/cibridge`, `internal/infra/mcp`, `internal/infra/httpapi`, `internal/infra/webhook`, `internal/app/orchestrator`, `cmd/beacon`, `internal/domain`, `docs/runbooks`, `docs/evals`, `docs/tasks`, `scripts`.

Тема не пересекается с `error-handling-audit.md` и `deadcode-audit.md`: поиск по ним имён `call_total`, `rss_bytes`, `fixers_spawned`, `wake_duplicate`, `MetricsFromSnapshot` дал 0 совпадений.

## Уточнение premise: file_adapter_reads_total

Метрики `file_adapter_reads_total` в репозитории нет. Поиск строк `file_adapter`, `FileAdapter`, `adapter_reads`, `reads_total` по всему дереву дал 0 совпадений. В `internal/infra/observe` нет ни одного счётчика чтений файлового адаптера. Ближайшие по смыслу метрики это `wake_delivered_total{adapter}`, `wake_errors_total{adapter}` и `wake_duration_seconds`. Ниже они разобраны по факту.

## Карта систем метрик

В репозитории три независимые системы, и они не связаны между собой.

1. `observe.Metrics` в `internal/infra/observe/metrics.go`. Процессные счётчики, гистограммы и gauge. Их читают `beacon metrics --format json|prometheus` (`cmd/beacon/observe.go:121`) и HTTP-эндпоинт `/metrics` через `StartMetricsServer` (`metrics.go:512`). Хелперы инкремента живут в `observe.go`.
2. `server.Metrics` в `internal/infra/server/metrics.go`. Счётчик `call_total`, гистограмма `call_duration_seconds`, gauge `rss_bytes` с замкнутыми метками `signal`, `reason`, `adapter`. В продакшн-коде не подключён.
3. Устойчивые счётчики в `internal/infra/store` и `internal/infra/ladder`: `persist_progress`, `ladder.pending/escalated`, `wake_observations`. Их показывает инструмент `beacon_stats` (`internal/infra/mcp/tool_stats.go:301`). Инструмент отдаёт `StatsOutput` с полями `pointsThisHour`, `budget`, `activeWatches`, `persist`, `loadErrors`, `ladder`, `version`, `registeredAgents`, `staleAgents`, `configID`. Ни одного счётчика из `observe.Metrics` в ответе нет.

`beacon_stats` поэтому не читает ни Prometheus-метрики, ни счётчики wake. Единственная связь с observe это запись `ActorError("beacon_stats", ...)` при ошибке чтения (`tool_stats.go:304`, `:341`). Число blocked-ватчей попадает в ответ строкой `blocked watches: N` внутри `loadErrors` (`tool_stats.go:315-323`), а не в отдельное поле. Eval E11 формулирует проверку как «счётчики совпадают с beacon_stats», но общего набора полей у этих двух источников нет.

### Реестр observe.Metrics

`counterLabels` в `metrics.go:19-32` задаёт 12 имён. `init()` в `observe.go:113` добавляет к ним `fix_total`. Итого `CounterNames()` возвращает 13 имён. `extendedCounterNames` (`metrics.go:43`) добавляет ещё два при экспорте: `fixers_spawned_total` и `blocks_escalated_total`.

| Имя | Хелпер | Точки инкремента | Метка | Экспорт | Тест |
| --- | --- | --- | --- | --- | --- |
| `poll_cycles_total` | `PollCycle`, `PollCycleStart` | `cibridge/bridge.go:528`, `observe.go:313,318` | нет | JSON, Prom | `coverage_test.go:12`, `observe_test.go:37`, `metrics_test.go:13` |
| `verdict_published_total` | `VerdictPublished`, `VerdictPublishedWithChecks` | `bridge.go:815,992,1125` | `verdict` (сырая строка домена) | JSON, Prom | `coverage_test.go:12`, `metrics_test.go:13`, `observe_test.go:37` |
| `merge_attempts_total` | `MergeAttempt` | `bridge.go:861`; `orchestrator.go:463` | нет | JSON, Prom | `coverage_test.go:12`, `observe_test.go:37` |
| `merge_success_total` | `MergeSuccess` | `bridge.go:864,884`; `orchestrator.go:530` | нет | JSON, Prom | `coverage_test.go:12`, `observe_test.go:37` |
| `merge_failures_total` | `MergeFailure` | `bridge.go:869,891`; `orchestrator.go:459,469,487` | нет | JSON, Prom | `coverage_test.go:12`, `cmd/beacon/observe_test.go:108` |
| `wake_delivered_total` | `WakeDelivered`, `WakeDeliveredSource` | `wake/deliver.go:949`; `bridge.go:1080,1266,1575`; `cmd/beacon/observe.go:281,283` | `adapter` | JSON, Prom | `coverage_test.go:12`, `metrics_test.go:13`, `observe_test.go:37`, `snapshot_test.go:8`, `cmd/beacon/observe_test.go:105` |
| `wake_errors_total` | `WakeFailed` | `deliver.go:944,951`; `bridge.go:1075,1261,1565,1702,1813`; `orchestrator.go:579`; `cmd/beacon/observe.go:272,277` | `adapter`, часто пустая | JSON, Prom | `coverage_test.go:12`, `metrics_test.go:48`, `observe_test.go:37` |
| `wake_skipped_total` | `WakeSkipped` | `deliver.go:913,920,926,935,953`; `bridge.go:1031,1041,1213,1220,1515,1525,1775`; `orchestrator.go:552` | нет, причина только в логе | JSON, Prom | `coverage_test.go:62`, `observe_test.go:37` |
| `wake_duplicate_total` | `WakeDuplicate` | `deliver.go:122,146` | нет | **нет** | нет |
| `rebase_requests_total` | `RebaseRequested` | `bridge.go:919`; `orchestrator.go:570` | нет | JSON, Prom | `coverage_test.go:12`, `observe_test.go:37`, `metrics_test.go:84` |
| `dependency_waits_total` | `DependencyWait` | `orchestrator.go:358` | нет | JSON, Prom | `coverage_test.go:62`, `observe_test.go:37` |
| `blocks_total` | `BlockDetected` | `bridge.go:548` | нет, причина только в логе | JSON, Prom | `coverage_test.go:62`, `observe_test.go:37` |
| `actor_errors_total` | `ActorError` | около 40 вызовов в `cmd`, `mcp`, `cibridge`, `webhook`, `httpapi` | нет, актор только в логе | JSON, Prom | `coverage_test.go:62`, `observe_test.go:37` |
| `fix_total` | `FixDelivered` | **нет вызовов в продакшн-коде** | `role=fixer` | JSON, Prom | `observe_test.go:78`, `coverage_test.go:12` |
| `fixers_spawned_total` | `FixerSpawned` | `orchestrator.go:421` | нет | JSON, Prom (extended) | `orchestrator_test.go:877,948`, `snapshot_test.go:15`, `tool_ack_test.go:1324` |
| `blocks_escalated_total` | `BlockEscalated` | `orchestrator.go:402`; `mcp/tool_ack.go:701` | нет | JSON, Prom (extended) | `orchestrator_test.go:904`, `tool_ack_test.go:1313` |

Гистограммы без меток: `poll_cycle_duration_seconds` пишут `PollCycleEnd` и `PollCycleDuration`, `merge_duration_seconds` пишут `MergeDuration` в `observe.go:423` и `bridge.go:850`, `wake_duration_seconds` пишут `WakeDuration` в `deliver.go:942` и `bridge.go:1574`, `run_duration_seconds` пишет `RunEnd` в `observe.go:584`. Адаптер в `WakeDuration` уходит только в поле лога, в метрику не попадает.

Gauge `active_watches` и `outbox_depth` объявлены в `metrics.go:49-50`. Хелперы `ActiveWatches` и `OutboxDepth` (`metrics.go:81,84`) вызываются только в тестах.

### Покрытие путей доставки wake

`T210Deliver` в `wake/deliver.go:903-970` разбирает пять исходов адаптера. `T210Delivered`, `T210Queued`, `T210Enqueued` идут в `wake_delivered_total`, `T210Errored` в `wake_errors_total`, остальное в `wake_skipped_total`. Ошибка транспорта на строке 944 тоже идёт в `wake_errors_total`. Все пять констант из `deliver.go:853-857` учтены.

Пути до вызова адаптера (`deliver.go:912-940`) считаются как `wake_skipped_total`: невалидная цель, нет адаптера, не удалось привязать цель, адаптер подставил чужую сессию. Дедупликация на in-memory карте (`deliver.go:120`) и в SQLite (`deliver.go:146`) вызывает `WakeDuplicate`.

Не учтён путь аренды цели. На `deliver.go:135-137` условие `if !acquired { return nil }` возвращается без всякого счётчика. Комментарий на строках 127-129 говорит, что это может быть как повтор того же владельца, так и отказ другому владельцу. Оба случая не видны ни в `wake_skipped_total`, ни в `wake_duplicate_total`, ни в логе.

## Дыры

### Почему wake_duplicate_total не появится в экспорте

`CounterWakeDuplicate` объявлен в `observe.go:101`, но отсутствует в `counterLabels`. `Metrics.Inc` (`metrics.go:194`) записывает неизвестное имя в `m.values` как простой total. `JSON()` (`metrics.go:306`) и `Prometheus()` (`metrics.go:434`) перебирают не `m.values`, а `CounterNames()`. `CounterNames()` строится из ключей `counterLabels`. Значение считается, но не попадает ни в JSON, ни в Prometheus, ни в снимок `runtime_stats`, потому что снимок это тот же `JSON()` через `SnapshotJSON`.

Тест `TestEveryCounterIsExportedAndIncrements` (`coverage_test.go:12`) не ловит ошибку: он перебирает тот же `CounterNames()`, а жёсткий список `want` на строках 39-45 тоже не содержит `wake_duplicate_total`. Eval R02 (`docs/evals/r02-same-pointer-once.md:11`) требует, чтобы счётчик рос, но проверить это нечем.

### Почему fix_total всегда нулевой

`FixDelivered` (`observe.go:458`) инкрементирует `fix_total{role=fixer}`. Единственный вызов хелпера находится в `observe_test.go:84`. В продакшн-коде вызова нет. Карточка T5.67 (`docs/tasks/T5.67.md:2`) прямо отложила точку вызова в `wake/deliver.go` на отдельную карточку ADR 0260. До её закрытия счётчик и событие `fix_delivered` в рабочем процессе не возникают.

### Почему два gauge всегда нулевые

`ActiveWatches(6)` и `OutboxDepth(11)` встречаются только в `cmd/beacon/observe_test.go:134-135,182-183` и в тестах пакета. В демоне, MCP-сервере и обработчиках нет ни одного вызова. Эндпоинт `/metrics` поэтому отдаёт `beacon_active_watches 0` и `beacon_outbox_depth 0` при любом числе вотчей. В `server.Metrics` есть свой gauge `rss_bytes`, но он тоже не подключён.

### fixers_spawned_total считает решения, а не спавны

`handleBlocked` в `orchestrator.go:399-422` вызывает `observe.FixerSpawned` на каждом проходе, пока вотч ниже лимита FIX-попыток. Комментарий на строках 394-398 и 419-420 прямо говорит, что в этом процессе нет прямого спавна фиксера и метка фиксирует решение. За комментарий отвечает тест `orchestrator_test.go:948`, где один и тот же сценарий даёт счётчик 2. Runbook (`docs/runbooks/observability.md:71`) описывает метрику как «FIX-агентов, запущенных для снятия блокировки». При тике демона в 300 мс один заблокированный вотч даёт рост счётчика без запущенного агента.

### Неучтённый отказ аренды цели

См. раздел «Покрытие путей доставки wake». Это единственный путь в `T210Deliver`, который не оставляет ни счётчика, ни события.

### Пустые значения меток

`WakeFailed` вызывается с пустым адаптером в `orchestrator.go:579`, `bridge.go:1702` и `bridge.go:1813`. В Prometheus это даёт серию `beacon_wake_errors_total{adapter=""}`. Причины пропуска в `wake_skipped_total` вообще нет в метрике, только в поле `reason` лога. То же относится к `blocks_total`. Eval E08 требует низкой кардинальности, поэтому добавлять `watchID` или `sessionID` нельзя. Пустую строку можно заменить фиксированным значением вроде `unknown`, не ломая кардинальность.

### Снимок метрик не читается командой beacon metrics

`ReconcilePass` сохраняет `metrics.SnapshotJSON()` в `runtime_stats` (`cmd/beacon/mcp.go:1377-1380`). `MetricsFromSnapshotJSON` определён в `metrics.go:362` и используется только в `snapshot_test.go`. `runMetrics` (`cmd/beacon/observe.go:127`) берёт метрики из текущего процесса через `observe.MetricsOf(observe.Default())` и не обращается к хранилищу. Комментарий в `metrics.go:349-353` утверждает обратное: что MCP пишет снимок, чтобы `beacon metrics` и `beacon inspect` показывали счётчики работающего процесса. Фактически `beacon inspect` действительно печатает сырой `RUNTIME_STATS` (`cmd/beacon/inspect.go:211`), а `beacon metrics` в отдельном процессе видит только то, что он сам записал. HTTP-эндпоинт работает верно, потому что живёт в том же процессе, что и демон.

### blocks_total считает обнаружения за тик

`BlockDetected` вызывается в `bridge.go:548` для каждого заблокированного вотча на каждом проходе моста. Один инцидент даёт рост на каждом тике, пока вотч не разблокирован. `blocks_escalated_total` и `blocked:not_mine` считают только первый ack (`tool_ack.go:686-701`), поэтому два счётчика блокировок измеряют разные вещи. Runbook называет `blocks_total` «обнаруженными блокировками Watch», не уточняя, что счётчик растёт на каждом проходе.

### Метрики серверного пакета не подключены

`internal/infra/server/metrics.go` содержит `CounterCalls = "call_total"`, `HistogramCallDuration`, `GaugeRSSBytes`, `FootprintCheck` и helper `labelString`. Поиск `IncCall(`, `ObserveDuration(`, `ObserveSeconds(`, `SetRSS(`, `FootprintCheck(`, `server.NewMetrics(` показал вызовы только внутри `internal/infra/server/metrics_test.go`. Продакшн-код не создаёт этот набор и не поднимает для него экспортер. Eval E11 (`docs/evals/e11-metrics-counters.md:9-10`) ожидает от `/metrics` счётчики и RSS в пределах цели, но `/metrics` принадлежит `observe.Metrics`, где этих серий нет.

### Метка verdict не совпадает с документацией

`VerdictPublishedWithChecks` (`observe.go:373-375`) пишет в метку сырое значение вердикта. Точки вызова передают `string(verdict)` из домена (`domain/types.go:26-38`), где есть `ready`, `retry`, `blocked`, `rebase`, `watching`, `keep_working`, `timeout`, `merged`, `closed`. Runbook (`observability.md:60`) обещает метки `ready`/`retry`/`fail`. Значения `fail` в домене нет, а `blocked` и другие вердикты в таблице не указаны. Параллельно `cycleTracker` в `observe.go:376-382` превращает всё, что не `ready`, в поле `retry` компактной строки `cycle`. Поэтому сводка цикла и метрика расходятся в классификации вердиктов.

### Расхождения JSON и Prometheus

Явного расхождения форматов в `JSON()` и `Prometheus()` для зарегистрированных имён нет: оба перебирают `CounterNames()`, extended-имена и `GaugeNames()`, оба читают гистограммы через `snapshot()` и те же границы `histogramBounds`. Отдельно стоит отметить два места:

- `wake_duplicate_total` отсутствует в обоих форматах, потому что не зарегистрирован. Это общая дыра, а не расхождение между экспортами.
- `fix_total` в JSON это объект `{role: count}`, в Prometheus это `beacon_fix_total{role="..."}`. Оба варианта корректны для метки.

## Итоговая таблица, топ-10

| Метрика | Путь | Покрытие | Дыра | Приоритет |
| --- | --- | --- | --- | --- |
| `wake_duplicate_total` | `deliver.go:122,146` → `WakeDuplicate` → `m.values` | Нет теста, нет в `CounterNames()` | Считается, но не экспортируется ни в JSON, ни в Prometheus, ни в снимок | P1 |
| `fix_total{role=fixer}` | `observe.go:458` `FixDelivered` | Юнит-тест хелпера `observe_test.go:78` | Нет ни одного вызова в продакшн-коде, счётчик всегда 0 | P1 |
| `active_watches`, `outbox_depth` | `metrics.go:81,84` `SetGauge` | Только тесты | Нет ни одного публикатора в демоне и MCP, оба gauge всегда 0 | P1 |
| `fixers_spawned_total` | `orchestrator.go:421` `handleBlocked` | `orchestrator_test.go:877,948` | Считает решения на каждом тике, а не запущенных агентов, хотя runbook называет их спавнами | P2 |
| `wake_errors_total{adapter}` | `orchestrator.go:579`, `bridge.go:1702,1813` | Тесты метрики есть, пустая метка не проверена | Серии с `adapter=""`, потеряна атрибуция отказа | P2 |
| Учёт аренды wake | `deliver.go:135-137` | Нет теста | `!acquired` возвращается без счётчика и события | P2 |
| Снимок `beacon metrics` | `cmd/beacon/observe.go:127`, `mcp.go:1378` | `cmd/beacon/observe_test.go:105` проверяет только in-process | Сохранённый `runtime_stats` не загружается, `MetricsFromSnapshotJSON` нужен только тестам | P2 |
| `blocks_total` | `bridge.go:548` `BlockDetected` | `coverage_test.go:62`, `observe_test.go:37` | Учёт detections за тик, а не отдельных инцидентов, причина не в метрике | P2 |
| `call_total`, `call_duration_seconds`, `rss_bytes` | `internal/infra/server/metrics.go` | Только собственные тесты | Нет продакшн-вызовов и экспортера, eval E11 не может пройти | P2 |
| `verdict_published_total{verdict}` | `bridge.go:815,992,1125` | `coverage_test.go:12`, `metrics_test.go:13` | Сырые вердикты домена вместо обещанных `ready/retry/fail`; расхождение со сводкой `cycle` | P3 |

Порядок в таблице отражает приоритет. P1 это невидимые или всегда нулевые метрики. P2 это неверная семантика или неподключённый экспортер. P3 это расхождение документации и кода без потери данных.

## Прочие наблюдения

- Eval E08 (`docs/evals/e08-metrics-stay-low-cardinality.md:10-11`) проверяет `provider_call_total` и `retry_open_duration_seconds`. Таких метрик в коде нет. `provider_call` это только лог-событие в `observe.go:622` и `ghci/provider.go:169`.
- Eval E01 требует, чтобы в `wake_delivered` были `watchID` и `sessionID`. `WakeDeliveredSource` (`observe.go:444`) пишет `adapter`, `watchID`, `source`, `revision`. Поля `sessionID` нет.
- Карточка T3.441 просит метку `source` в счётчиках wake. Реализация T3.417 оставила `source` только полем лога. Причина, по которой метку нельзя добавить без чужих OwnerFiles, зафиксирована в самой карточке. Два пути с `adapter=opencode` остаются неразличимыми в `wake_delivered_total`.
- `cycleTracker.wakes` (`observe.go:447,480,505`) увеличивается при delivered, skipped и failed, но не при duplicate. Поле `wakes` в строке `cycle` поэтому не равно `wake_delivered_total`. Это не ошибка экспорта, но легко читается как одно и то же число.
- `Metrics.Inc` (`metrics.go:200-204`) создаёт запись в `m.values` для любого неизвестного имени. Для gauge есть защита `knownGauges`, для счётчиков такой проверки нет. Публичный `Count(name, value)` (`observe.go:271`) поэтому может расширять внутренний map без ограничения, хотя экспортировать эти имена всё равно не сможет.
- `MetricsFromSnapshotJSON` (`metrics.go:381-384`) восстанавливает меченые счётчики в `m.labelled`, но оставляет `m.values[name]` равным нулю. Экспорт читает `m.labelled`, поэтому видимого расхождения нет. Если после восстановления вызвать `Inc` на меченом счётчике, внутренний total начнёт считаться с нуля.
- Таблица в `docs/runbooks/observability.md:57-74` не упоминает `wake_duplicate_total`, `fix_total`, `active_watches` и `outbox_depth`. Скрипт `scripts/verify-beacon.sh:232-245` проверяет наличие 12 фиксированных ключей и не проверяет значения.

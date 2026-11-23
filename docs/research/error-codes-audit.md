# Аудит кодов ошибок, вердиктов и статусов

Дата: 2026-11-21. Репозиторий: `beacon-impl`, коммит `d3b1bfb1` (main).

## Как снимался замер

Исследование read-only. Код не менялся. `go test`, `go build` и `beacon gates` не запускались. Все факты получены из исходников через `rg` и `git grep`. В подсчёты главного дерева (main) не входят worktrees и ветки, если это явно не оговорено. Для кодов из других веток приведены хеши коммитов и результат проверки `git merge-base --is-ancestor`.

Под «кодом» здесь понимается машиночитаемая строка, на которую вызывающий код ветвится: вердикт, статус, причина, код ошибки, правило гейта, уровень. Человеческие подписи в сообщениях в реестр не входят, кроме случаев, где подпись пишется в поле `Reason` и читается программой.

## Итог: единого реестра нет

Единого файла, который описывал бы все коды, в репозитории нет. Есть четыре частичных реестра в документации:

- `GLOSSARY.md:39-40` и `README.md:133-159` перечисляют вердикты Watch.
- `docs/api/openapi.yaml:341-352` перечисляет шесть примеров кодов HTTP-ошибок.
- `docs/RULES-CHECK-CONTRACT.md:40-44` задаёт `pass`/`warn`/`fail` и `PASS`/`WARN`/`FAIL` для отчёта правил.
- `tools/beacon-gates/gate/config/*.json` хранит.baseline и постоянные исключения по правилам гейтов.

В коде реестров больше. Ниже они все перечислены по семействам.

## Реестр 1. Вердикты Watch, канонический набор

Определение: `internal/domain/types.go:23-39`, тип `Verdict`.

| Код | Где определено | Пишет | Читает | Документация |
| --- | --- | --- | --- | --- |
| `watching` | `types.go:27` | `domain.Evaluate` (`verdict.go:114,151,161`), `EvaluatePR`, `Transition` | `cibridge/bridge.go:757`, `tool_get_result.go:388`, store | GLOSSARY.md:40, README.md:137 |
| `ready` | `types.go:28` | `Evaluate:146`, `EvaluatePR:222` | `cibridge/bridge.go:761` (мерж), `tool_get_result.go:378`, оркестратор | GLOSSARY.md:40, README.md:3,137 |
| `rebase` | `types.go:29` | `Evaluate:120`, `EvaluatePR` | `cibridge/bridge.go:1868`, `workload.go:95`, `tool_get_result.go:369` | GLOSSARY.md:40, README.md:137 |
| `retry` | `types.go:30` | `Evaluate:128`, `EvaluatePR` | `cibridge/bridge.go:1870`, `workload.go:95`, `tool_get_result.go:372` | GLOSSARY.md:40, README.md:137 |
| `keep_working` | `types.go:31` | `Evaluate:133`, `EvaluatePR` | `cibridge/bridge.go:1859`, `workload.go:95`, `tool_get_result.go:366` | GLOSSARY.md:40, README.md:137 |
| `timeout` | `types.go:32` | `Evaluate:156` | `cibridge/bridge.go:1883`, `workload.go:95`, `tool_get_result.go:381` | GLOSSARY.md:40, README.md:137 |
| `merged` | `types.go:33` | `Evaluate:104`, `EvaluatePR` | `cibridge/bridge.go:766`, `store.CloseMergedWatch` | GLOSSARY.md:40, README.md:137 |
| `closed` | `types.go:34` | `Evaluate:106`, `EvaluatePR` | `cibridge/bridge.go:768` | GLOSSARY.md:40, README.md:137 |
| `blocked` | `types.go:38` | `EvaluatePR:202`, `transition.go:345` (EventBlock) | `cibridge/bridge.go:759,1883`, `tool_get_result.go:375`, `tool_ack.go:784` | GLOSSARY.md:40, README.md:137 |
| `needs_input` | `transition.go:23` | `transition.go:306` (EventNeedsInput) | `cmd/deliver.go:273`, `tool_wait_result.go:1318`, TUI | GLOSSARY.md:40, README.md:219 |
| `parked` | `transition.go:35` | `transition.go:323` (EventPark) | `Transition` guards (`transition.go:404`) | GLOSSARY.md:40, README.md:137 |
| `accepted` | `transition.go:40` | `transition.go:377` (EventAccept) | `validVerdict` отклоняет (`transition.go:442`) | GLOSSARY.md:40, README.md:137 |
| `green_blocked` | `verdict.go:883`, тип `GreenBlockedVerdict` | `EvaluateGreenBlocked:910` | `ReleasesSlot:891`, слоты оркестратора | **нет в GLOSSARY.md и README.md** |

Дополнительно: `validVerdict` (`transition.go:442-450`) принимает только первые восемь значений из таблицы. `blocked`, `needs_input`, `parked` и `accepted` приходят через отдельные события (`EventBlock`, `EventNeedsInput`, `EventPark`, `EventAccept`), минуя проверку вердикта.

## Реестр 2. Статусы, причины и wake-события Watch

| Код | Где определено | Пишет | Читает |
| --- | --- | --- | --- |
| `watching`, `notifying`, `blocked`, `closed` | `types.go:16-21`, тип `Status` | `Transition` (`transition.go:306,323,345,377`) | `cibridge`, `tool_list_running.go:567`, CLI |
| `needs_input`, `parked` | `transition.go:18,31` | `Transition` | CLI, TUI, `tool_wait_result.go:1329` |
| `head_changed`, `cancelled`, `merged`, `closed`, `accepted` | `transition.go:84-92`, тип `CloseReason` | `cibridge/bridge.go:679`, `tool_cancel_watch.go:210`, `Transition` | CLI inspect, store |
| `limit`, `budget`, `crash`, `error` | `transition.go:46-51`, тип `ParkReason` | EventPark | журнал парковки |
| `verdict`, `cancel`, `head_changed`, `needs_input`, `continue`, `park`, `accept`, `block`, `unblock` | `transition.go:57-79`, тип `EventKind` | адаптеры | `Transition` |
| `wake_subscribers`, `create_watch`, `continue_watch`, `log_park` | `transition.go:120-132`, тип `EffectKind` | `Transition` | адаптеры |
| `head_changed`, `rest_pin`, `rerun_checks` | `verdict.go:78-85`, тип `WakeKind` | движок вердикта | `cibridge`, wake-адаптер |
| `not_mine` | `tool_ack.go:82`, `BlockedInfo.Reason` | `tool_ack.go:643` | `up.go:407`, `inspect.go:194`, TUI, `orchestrator.go:155` |
| `resolved` | `tool_ack.go:83` | нигде, только case в `tool_ack.go:553` | `blockedDecisionReason` |
| `failed checks already present on base branch` | `cibridge/bridge.go:1096` | `applyBlocked` | тот же `BlockedInfo.Reason` |
| `escalate_human` | `decision.go:46`, `wake/notify.go:19` | `orchestrator.go:402`, `tool_ack.go:701` | `wake/telegram.go:307` |
| `executor_orphaned`, `blocker_opened` | `wake/notify.go:19-21` | `cibridge/bridge.go:1707` | `wake/telegram.go:312` |

## Реестр 3. Вердикты-зеркала в app и cmd

Эти строки дублируют доменные значения, потому что `internal/app` не может импортировать `internal/domain` (ADR 0005).

| Код | Где определено | Кто использует |
| --- | --- | --- |
| `watching`, `ready`, `retry` | `checkall.go:31-36`, тип `Verdict`, тип локальный | `checkall.go:415,435,452,805,829` |
| `ready`, `wait`, `wait_dependency`, `retry`, `fail` | `orchestrator.go:21-36` | `orchestrator.go:176,186,194,349` |
| `wait_dependency:<reason>` | `orchestrator.go:174-178` | `isWaitDependency:186`, store Watch rows |
| `rebase` | `rebase.go:85` | rebaser |
| `watching`, `rebase`, `keep_working`, `timeout` | `workload.go:31-34` | `needsAttention:94` |
| `rebase`, `retry`, `keep_working` | `daemon.go:284-290`, строки в switch | `daemonWakeInstruction` |
| `ready`, `needs_input` | `cmd/deliver.go:82-90` | `deliver.go:264,273` |
| `needs_input` | `cmd/watch.go:384` | отрисовка таблицы |
| `merged` | `app/orchestrator/store.go:140,159,168` | raw строки, без типа |
| `keep_working`, `rebase`, `retry`, `blocked`, `wait` | `mcp/schema.go:83-89`, `NextAction` | `tool_get_result.go:361-386` |

## Реестр 4. Решения `beacon_ack` и остановы

| Код | Где определено | Пишет | Читает | Документация |
| --- | --- | --- | --- | --- |
| `approve`, `reject` | `tool_ack.go:58-63`, тип `Decision` | `tool_ack.go:569`, `DecisionRecord` | `Decision.Valid:98`, демоны | README.md:60 |
| `blocked:not_mine` | `tool_ack.go:68` | клиент, `AckReceipt.Decision` | `blockedDecisionReason:89`, dispatch `:541` | README.md:60,165, AGENTS.md |
| `blocked:resolved` | `tool_ack.go:72` | клиент, `AckReceipt.Decision` | dispatch `:553` | README.md:60 |
| `blocked:` | `tool_ack.go:77`, префикс | нет | `blockedDecisionReason:89` | нет |
| `terminal`, `input_required`, `unknown_submission`, `timeout`, `interrupted` | `tool_wait_result.go:95-108`, тип `WaitStopReason` | wait_result | `valid:111`, `ExitCode:127`, `Retryable:144` | ADR 0046 |
| `caller_cancelled`, `session_closed`, `connection_broken`, `caller_context_ended` | `tool_wait_result.go:522-532`, тип `CancelReason` | cancellation journal | `reasonErrorCode:536` | ADR 0040 |
| `precheck_failure` | `tool_wait_result.go:599` | `PrecheckFailureRecord` | клиенты wait_result | ADR 0041 |
| `selection_changed`, `active_obligations` | `cmd/beacon/registry_stop.go:25-28` | `checkRegistryStop` | CLI, тесты | ADR 0091 |
| `stop_uncertain`, `stopPhase` | **только ADR 0091:15, docs/tasks/T5.6.md:12, T5.52.md** | нет в main | нет в main | ADR 0091 |

`ACTOR_NOT_PERMITTED` в этот реестр main не входит. Строка определена в коммите `29da86b6` (`T3.284: per-app credentials gate human-decide actors (#1136)`), который не является предком `d3b1bfb1`. Там же определено `UNSUPPORTED_CONTRACT`, но в main оно есть отдельно в `httpapi/server.go:952`. Карточка `docs/tasks/T3.284.md:2` всё ещё имеет статус `todo`.

## Реестр 5. Коды ошибок, шесть независимых определений

| Код | Где определено | Комментарий |
| --- | --- | --- |
| `INVALID_INPUT`, `WATCH_NOT_FOUND`, `HEAD_SHA_CHANGED`, `RATE_LIMITED`, `LIMIT_EXCEEDED`, `CANCELLED`, `GONE`, `PERSIST_FAILED` | `domain/types.go:500-509`, тип `ErrorCode` | Канонический набор. `statusForCode:525` |
| `REJECT_DUPLICATE` | `domain/identity.go:63` | Статус 409 задан в конструкторе `:68`, в таблицах статусов отсутствует |
| `CI_UNKNOWN_PROVIDER`, `CI_MALFORMED`, `CI_MISSING_STATUS`, `CI_INVALID_STATUS` | `infra/github/errors.go:370-373` | Тип `domain.ErrorCode`, но определено в infra. Статус 400 ставит `ciAppError:377` |
| `INVALID_INPUT`, `PROVIDER_NOT_BOUND`, `GIT_FAILED`, `CARD_FAILED`, `CREATE_FAILED`, `WATCH_FAILED` | `app/prcreate/service.go:15-23` | Свой тип `ErrorCode`. `INVALID_INPUT` совпадает с доменным по строке, но не по типу |
| `invalid_argument`, `not_found`, `method_not_allowed`, `unauthorized`, `forbidden`, `conflict`, `internal` | `infra/server/http/error.go:14-24`, тип `Code` | Нижний регистр. `Status:48`, `CodeOf:69`, `WriteError:83` |
| `RECLAIM_GRANTED`, `RECLAIM_REFUSED_LIVE`, `RECLAIM_REFUSED_UNVERIFIABLE` | `infra/mcp/tool_list_running.go:175-180` | Свой тип `OwnerReclaimCode`, намеренно не доменный |
| `UNAUTHORIZED`, `INVALID_INPUT`, `FORBIDDEN`, `UNSUPPORTED_CONTRACT`, `METRICS_FAILED`, `SNAPSHOT_FAILED`, `STREAM_UNSUPPORTED`, `DECISION_FAILED` | `infra/httpapi/server.go:674-967`, raw строки | Таблица статусов задается аргументом `writeError:730` |
| `METHOD_NOT_ALLOWED`, `NOT_FOUND`, `WATCHES_FAILED`, `AUDIT_FAILED`, `STORE_FAILED`, `COMMAND_FAILED`, `AUDIT_FAILED` | `infra/server/api_*.go`, `scheduler.go`, raw строки | Верхний регистр |
| `method_not_allowed`, `missing_delivery_id`, `bad_body`, `bad_json`, `journal_failed`, `hint_failed`, `invalid_signature`, `body_too_large`, `duplicate`, `accepted` | `infra/webhook/*.go`, raw строки | Нижний регистр и snake_case |

Две таблицы статусов для одного и того же доменного набора: `domain/types.go:525-542` (switch) и `infra/mcp/schema.go:372-380` (map). Вторая явно объявлена зеркальной. `ToErrorShape` (`schema.go:393-412`) берёт статус из map, а при нуле из `appErr.Status`. По состоянию на main у `ToErrorShape` нет продакшен-вызова, только `ToRepairableErrorShape` в `tool_wake.go:443`, у которого тоже нет продакшен-вызова.

## Реестр 6. Правила гейтов, уровни и статусы отчётов

| Код | Где определено | Механизм |
| --- | --- | --- |
| `Green=0`, `Red=1`, `Broken=2` | `tools/beacon-gates/gate/gate.go:16-19` | exit code, вывод `green:` / `red:` / `broken:` |
| `DOC-TASK-HEADING`, `DOC-TASK-STATUS`, `DOC-TASK-FIELD`, `DOC-ADR-HEADING`, `DOC-ADR-STATUS`, `DOC-ADR-STRUCTURE`, `DOC-ADR-DUP`, `DOC-BASELINE-EXCEEDED`, `DOC-BASELINE-STALE`, `DOC-LINK-PATH`, `DOC-LINK-ANCHOR` | `docsgate/docs.go:193-295`, `gate/gate.go:197`, inline | finding.Rule |
| `LAYER-FORBIDDEN-EDGE`, `LAYER-NO-RULE`, `LAYER-UNUSED-RULE`, `LAYER-DIVERGENCE` | `gate/layers.go:367,384,395`, `layers-ast/layersast.go:29` | finding.Rule |
| `REACH-UNWIRED`, `REACH-TEST-IGNORE`, `REACH-TEST-SKIP`, `REACH-TEST-PKG`, `REACH-TEST-PARSE`, `REACH-PARSE` | `reachability/reachability.go:20-28` | finding.Rule |
| `TOOL-NAME-FORMAT`, `TOOL-NAME-DUPLICATE`, `TOOL-DESC-SHORT`, `TOOL-PARAM-DESC`, `TOOL-REQUIRED`, `TOOL-ADDITIONAL-PROPS`, `TOOL-DESC-NEAR-DUP`, `TOOL-LIST-INVALID` | `gate/tool_contract.go:13-20` | finding.Rule |
| `WIRING-STATUS`, `WIRING-OWNERFILES`, `WIRING-REASON` | `gate/wiring.go:107,119,127` | finding.Rule |
| `SCAN-BLIND` | `gate/gate.go:166` | finding.Rule при findings меньше floor |
| `CAP-<EXEC/FILE/NETHTTP/NETDIAL/PLUGIN/REFLECT/UNSAFE>-IN-<LAYER>` | `capabilities/capabilities.go:95`, таблица `:25-33` | правило собирается динамически |
| `pass`, `warn`, `fail` | `docs/RULES-CHECK-CONTRACT.md:42`, `cmd/beacon/commands.go:61-72` | машинный статус отчёта правил |
| `PASS`, `WARN`, `FAIL` | `docs/RULES-CHECK-CONTRACT.md:42`, `cmd/beacon/doctor.go:176-215` | человеческий вердикт отчёта |
| `critical`, `high`, `medium`, `low` | `evals/cmd/evalserver/main.go:62-75` | security severity |
| `error`, `warning` | `evals/cmd/evalserver/main.go:78-79` | gate level, маппинг `:92-99` |
| `info`, `warn`, `error`, `debug` | `infra/observe/observe.go:18-40` | `slog.Level`, `ParseLevel` |
| `ok`, `retry`, `error`, `blocked`, `Other` | `infra/server/metrics.go:56-59` | метка `reason` |

Отдельно: `BROKEN` как строка в main отсутствует. `Broken` это имя константы exit code 2. `WARN` как код в main отсутствует. Есть `LevelWarn`, `warn` в отчёте правил и префикс `"WARNING: ..."` в `infra/store/observability.go:527` и `infra/server/metrics.go:375`.

## Расхождения

### P0. Один и тот же literals `timeout` несёт два разных контракта

`domain.VerdictTimeout` (`types.go:32`) означает дедлайн или таймаут без проверок. `nextStepFor` (`tool_get_result.go:381-382`) возвращает для него `nextAction="wait"` и подсказку «the Watch is finished; there is no next step».

`WaitStopTimeout` (`tool_wait_result.go:106`) означает, что локальный long-poll истёк. Он даёт exit code 143 (`:134`) и `Retryable() = true` (`:144-146`).

Обе строки приходят одному и тому же агенту через MCP. Клиент, который ветвится только на строку `timeout`, перепутает терминальный вердикт с повторяемым ожиданием. Здесь нужен либо префикс, либо явное поле источника в контракте.

### P0. Вердикт живёт в семи наборах констант, и они расходятся по составу

Канонический набор в `domain/types.go` не содержит `fail`, `wait`, `wait_dependency`, `green_blocked`. Все четыре существуют в app или в отдельном типе `GreenBlockedVerdict`. `checkall.go:29-30` прямо называет свой тип «mirror for the layer boundary», но зеркало не полное.

`validVerdict` (`transition.go:442`) не принимает `blocked`, `needs_input`, `parked`, `accepted`. Эти значения пишутся обходом через события. Оркестратор пишет составное `wait_dependency:<reason>` (`orchestrator.go:174-178`) в то же поле `Verdict` Watch. Ни один доменный тип такую строку не разбирает. Изменение доменного enum не меняет поведение app-зеркал.

### P0. Два кода из ADR реализованы в ветках, но отсутствуют в main

`stop_uncertain` с полем `stopPhase` описан в `ADR 0091:15` и в карточках `T5.6`, `T5.52`. Коммит `13f8558c` (`T5.52: report uncertain registry stop with stopPhase (#1107)`) реализует его, но `git merge-base --is-ancestor 13f8558c HEAD` даёт отрицательный результат. В main живёт только precondition `checkRegistryStop` с причинами `selection_changed` и `active_obligations`. `docs/tasks/T5.52.md:4` всё ещё `todo`.

`ACTOR_NOT_PERMITTED` реализован в коммите `29da86b6`, тоже не предке HEAD. Карточка `T3.284.md:2` тоже `todo`. Побочный эффект: `UNSUPPORTED_CONTRACT` в main есть, а его парная проверка actor'а нет. Для читателя контракта это выглядит как полная пара, хотя на main работает только половина.

### P1. Причина блокировки не имеет закрытого набора значений

Поле `BlockedInfo.Reason` (`domain/types.go:191-192`) объявлено как `string`. В него пишут:

- `not_mine` из `tool_ack.go:643`, машиночитаемый код;
- `failed checks already present on base branch` из `cibridge/bridge.go:1096`, предложение;
- `not mine` в тестах `infra/wake/notify_test.go:364`, `cmd/beacon/up_test.go:115,136,145`.

`blockedReasonResolved = "resolved"` (`tool_ack.go:83`) определено, но ни разу не пишется в `Reason`, только используется как case в dispatch. Потребители не сравнивают причину с кодом: `up.go:407`, `inspect.go:194`, TUI печатают её как есть. Единственное сравнение в `wake/telegram.go:307` проверяет совсем другой код `escalate_human`.

### P1. Коды ошибок распределены по шести типам и двум регистрам строк

Тип `domain.ErrorCode` объявлен в `types.go:498`, но новые коды добавляются в `github/errors.go:370`, `prcreate/service.go:15` и `tool_list_running.go:175`. `server/http/error.go:14` объявляет ещё один тип `Code` со строчными значениями. `httpapi/server.go` и `api_*.go` используют raw строки в верхнем регистре, `webhook/*.go` в нижнем.

Один и тот же смысл кодируется по-разному: `method_not_allowed` в `httpapi`-совместимом типе (`server/http/error.go:19`) и `METHOD_NOT_ALLOWED` в `api_watches.go:187`, `api_wake.go:281`, `api_audit.go:238`, плюс `method_not_allowed` в webhook. `not_found` и `NOT_FOUND` тоже сосуществуют.

`statusForCode` и `statusByCode` дублируют одну таблицу. Коды `REJECT_DUPLICATE`, `CI_*`, `RECLAIM_*` в обе таблицы не включены. Для них `StatusForCode` возвращает 0. Конструкторы пока ставят статус вручную, но новый код, созданный через `NewAppError`, получит статус 0, что не является корректным HTTP-статусом, если код занести только в `AppError`.

### P1. `fail` и `warn` документированы как вердикты, но в домене их нет

`docs/runbooks/observability.md:60` обещает метку `verdict_published_total{verdict}` со значениями `ready`/`retry`/`fail`. В домене `fail` нет. В app он есть как `VerdictFail` (`orchestrator.go:35`), и `needsWake:194` на нём ветвится. Метрика пишется из доменного вердикта, поэтому `fail` в ней никогда не появится.

`docs/RULES-CHECK-CONTRACT.md:42` использует пару регистров для одного смысла: `WARN` в тексте отчёта и `warn` в JSON. `cmd/beacon/commands.go:61-72` реализует только строчные значения. `cmd/beacon/doctor.go` использует только верхний регистр `FAIL`/`OK`. Exit code `Broken=2` из гейтов не связан ни с одной из этих шкал.

### P1. Правила гейтов не собраны в одном месте

Имена правил объявлены четырьмя способами: константы в `tool_contract.go`, `reachability.go`, `wiring.go`; inline-строки в `docsgate/docs.go` и `gate/layers.go`; динамическая сборка `CAP-* + "-IN-" + layer` в `capabilities/capabilities.go:95`; строковые ключи в `gate/config/*.json`. `gate.Finding.Rule` (`gate.go:23`) это просто `string`, и новый код правила ничем не проверяется на уникальность или известность.

### P2. Две копии функции нормализации результата проверки

`domain/verdict.go:244-256` (`NormalizeConclusion`) и `app/orchestrator/checkall.go:703-713` (`normalizeConclusion`) содержат одинаковые списки `PASS/SUCCESS/SUCCESSFUL/SKIPPED/NEUTRAL` и `FAIL/FAILURE/ERROR/TIMED_OUT/ACTION_REQUIRED`. Частичные копии есть в `infra/github/batch_provider.go:597` и `infra/cibridge/bridge.go:616`. Это те же «разные коды для одного смысла», только скомпенсированные маппингом. `CANCELLED` и `CANCELED` в обоих списках нормализуются в одно значение осознанно.

### P2. Опечатки и варианты написания

- `not mine` в тестах вместо `not_mine` (`notify_test.go:364`, `up_test.go:115,136,145`).
- `head changed` вместо `head_changed` в `docs/gates/G2-canary.md:73`, где речь идёт о `CloseReasonHeadChanged`.
- `green-blocked` в комментариях и именах тестов (`verdict_test.go:1497,1555`) против `green_blocked` в коде.
- `method_not_allowed` против `METHOD_NOT_ALLOWED`, `not_found` против `NOT_FOUND`.
- `WARNING` как строка встречается только в assertions тестов (`metrics_test.go:263`, `observability_test.go:570`), в проде это префикс внутри более длинной строки.

### P2. Коды только в тестах

Сканирование строк вида `UPPER_SNAKE` и `UPPER-DASH` по `*_test.go` против нетестовых файлов дало около ста совпадений. Почти все они являются фикстурами (`HD-001`, `WIRING-42`, `BS-10`, `RULE-A`, `APPROVE`, `CRITICAL`, `WARNING`). Реальный машинный код, который встречается только в тестах и расходится с продовым, один: `not mine` из предыдущего пункта. `ACTOR_NOT_PERMITTED` тоже встречается в тесте, но этот тест живёт в ветке `fix/idx05`, а не в main.

Отдельно стоит `green_blocked`: код есть в продакшене, но отсутствует в обоих списках вердиктов в документации.

## Что делать по приоритету

1. Развести `timeout` в `get_result` и `wait_result` по разным значениям или добавить поле источника. Сейчас один literals даёт противоположные инструкции.
2. Собрать все вердикты и причины в один файл контракта и сделать app-зеркала производными от него. Включить `fail`, `wait`, `wait_dependency`, `green_blocked`, все blocked-причины и stop-причины.
3. Решить судьбу `stop_uncertain` и `ACTOR_NOT_PERMITTED`: либо влить коммиты `13f8558c` и `29da86b6`, либо явно пометить карточки и ADR как нереализованные. Сейчас документация обещает код, которого нет в main.
4. Дать `BlockedInfo.Reason` тип с закрытым набором и убрать предложение из `cibridge/bridge.go:1096`.
5. Объединить статусные таблицы или явно запретить новые коды вне доменного реестра. Добавить проверку, что `StatusForCode` не возвращает 0 для известных кодов.
6. Привести написание `not mine`, `head changed`, `method_not_allowed` к продовым значениям в тестах и документации.

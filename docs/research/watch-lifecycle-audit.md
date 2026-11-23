# Аудит жизненного цикла Watch: blocked, rebase, retry

Дата: 2026-11-21. Репозиторий: `beacon-impl`, основная ветка `main` на коммите `d3b1bfb1`. Режим: только чтение. Код не менялся, `go test`, `go build` и `beacon gates` не запускались.

## Методика

Поиск шёл через `rg` с явными каталогами `internal`, `cmd`, `docs`, `evals`. Каталоги `worktrees/` и `tmp/` исключены: это рабочие копии и временные файлы. Серверная часть `cibridge` читалась в двух видах. На `main` это один файл `internal/infra/cibridge/bridge.go`. После ARDH-11, коммит `24eaff0a`, мост разделён на `ci_refresh.go`, `verdict_applier.go`, `wake_dispatcher.go`, `read_api.go` и `components.go`. Разделённые файлы прочитаны из ветки `fix/ardh11-split-bridge` через `git show` без переключения дерева. Ветка ушла вперёд и содержит ARDH-12, ARDH-14 и ARDH-15, поэтому часть типов там отличается от `main`. В отчёте я различаю эти две версии явно.

Примеры из сессии (#1112, #1111, #1105) в репозитории не лежат. Транскрипт сессии в локальном хранилище поиском не найден. Разбор этих примеров опирается на код и на описание симптомов из задания, а не на прочитанный лог сессии.

## Карта lifecycle

Восемь MCP-инструментов описаны в `internal/infra/mcp`: `beacon_watch_pr` регистрирует Watch и подписчиков, `beacon_get_result` выдаёт вердикт и ставит `readAt`, `beacon_wait_result` держит long-poll, `beacon_ack` ставит `claimedAt` и принимает `decision`, остальные инструменты читают состояние и управляют пробуждением. Контракт `decision` включает `approve`, `reject`, `blocked:not_mine` и `blocked:resolved`; последние два идут отдельной веткой и не возобновляют агента (`internal/infra/mcp/tool_ack.go:96-100`, `:539-565`).

Мост после ARDH-11 состоит из четырёх частей. `ciRefresh` читает live Watch и нормализует ответы CI. `verdictApplier` применяет результат `domain.EvaluatePR`: публикует вердикт, мержит, блокирует, закрывает, снимает блок. `wakeDispatcher` держит доставку, счётчик wake, breaker адаптера и watchdog. `readAPI` обслуживает оркестратора. Все четыре части встраивают `*Bridge`, поэтому публичные сигнатуры не изменились (`internal/infra/cibridge/components.go:3-24`).

Движок вердиктов чистый. `Evaluate` и `EvaluatePR` читают только `Snapshot` и `Policy` (`internal/domain/verdict.go:100-162`, `:170-237`). `Transition` чистая и не пишет время (`internal/domain/transition.go:254-295`). Блокировка и разблокировка идут через `BlockEvent` и `UnblockEvent` и не создают wake-эффектов (`internal/domain/transition.go:334-364`). Эскалация доставки живёт отдельно в `internal/infra/ladder/escalation.go`, эскалация FIX-агентов в `internal/app/orchestrator/decision.go` и `orchestrator.go`.

## Пять главных находок

### 1. Информационная проверка с `required: false` даёт `keep_working`

AGENTS.md:113 и шаблоны прямо запрещают считать такую проверку блокером. Код этого запрета не знает. `checkFacts` в `internal/infra/cibridge/bridge.go:608-629` перебирает все проверки и не смотрит на поле `Required`. Любой `FAILURE`, `ERROR` или `CANCELLED` переворачивает `rollup` в `FAILURE` и добавляет имя в `failures`. Движок `EvaluatePR` в `internal/domain/verdict.go:205-213` фильтрует только conclusion проверки через `harmlessConclusion`, а не её обязательность. Итог: упавший informational-гейт даёт `VerdictKeepWorking` и wake исполнителю с текстом "чини конкретные failedChecks" из `wakeText` (`wake_dispatcher.go:609-617` на разделённой ветке, тот же текст на `main` в `bridge.go`).

Фикстурный контракт в `internal/infra/github/fixtures_contract_test.go:225-260` читает `rollup` из `tier1.Commits.Nodes[0].Commit.StatusCheckRollup.State`, а не из отдельных проверок. Поэтому расхождение между GitHub rollup и локальным `checkFacts` тестами не ловится.

### 2. Бюджет FIX-агентов не работает, `escalate_human` недостижим

`Decide` в `internal/app/orchestrator/decision.go:226-236` сравнивает `w.Blocked.FixAttempts` с `MaxFixAttempts = 3` и возвращает `DecisionNotifyOrchestrator` с `ReasonEscalateHuman` только при трёх попытках. В продакшене `Watch.Blocked.FixAttempts` не увеличивает никто. `handleBlocked` в `internal/app/orchestrator/orchestrator.go:399-419` намеренно не пишет счётчик, чтобы трёхминутный тик не списывал попытку. `RecordFixAttempt` объявлен в `internal/app/orchestrator/store.go:107` и в интерфейсе `Store`, но вызовов вне тестов нет. `AssignFixer` в `internal/infra/store/blockers.go:209-231` увеличивает счётчик инцидента, а не счётчик в `BlockedInfo` Watch. `projectBlocked` в `cmd/beacon/orchestrate.go:139-149` берёт значение из Watch. Два счётчика не связаны. Пока фиксеры не регистрируются, решение остаётся `BLOCKED_FIX_AGENT` бесконечно. ADR 0260 требует границы цикла, код её не даёт.

### 3. Таймаут без проверок мёртв, а дедлайн теряется на преемнике

В `domain.Snapshot` есть `NoChecksSince` и `NoChecksTimeout` (`internal/domain/verdict.go:52-54`). Правило 8 в `EvaluatePR`, `internal/domain/verdict.go:230-233`, требует оба поля. `Watch` хранит только `NoChecksSince` (`internal/domain/types.go:262-263`). Ни один продакшен-путь не пишет `NoChecksSince` в Watch, а `snapshotFor` не заполняет `NoChecksTimeout` ни на `main` (`bridge.go:640-659`), ни на разделённой ветке (`ci_refresh.go:133-155`). Проверка `s.NoChecksTimeout > 0` в `timedOut` (`verdict.go:315-318`) всегда ложна. Дедлайн по `DeadlineAt` работает, но `handleHeadChanged` создаёт Watch-преемника без `DeadlineAt`, `NoChecksSince`, `BlockerID`, `FixBlockerID` и `ConfigID` (`bridge.go:694-712` на `main`, `verdict_applier.go:45-61` на разделённой ветке). После force-push дедлайн исчезает вместе со старым Watch.

### 4. Заблокированный Watch не закрывается при смене head, а повторная регистрация перезаписывает блок

`Refresh` обрабатывает `StatusBlocked` раньше проверки head и делает `continue` (`bridge.go:539-559` на `main`, `bridge.go:590-615` на разделённой ветке). Поэтому заблокированный Watch с новым `headRefOid` остаётся заблокированным и не получает преемника. `handleBlocked` не сравнивает SHA вообще (`verdict_applier.go:745-776`).

Второй путь хуже. `closeSuperseded` в `internal/infra/mcp/tool_watch_pr.go:679-711` берёт Watch только из `ActiveWatches`, а `ActiveWatches` возвращает `watching` и `notifying` (`internal/infra/store/db.go:386-392`). `Fanout.Subscribe` тоже ищет алиас только среди active (`internal/domain/fanout.go:92-113`, `:184-195`). Если executor регистрируется заново на новый SHA, пока старый Watch в `blocked`, активного Watch нет, создаётся новый Watch, а старый остаётся в blocked с открытым инцидентом. Если headSHA тот же, `normalizeWatch` выводит тот же ID и `SaveWatch` перезаписывает заблокированную запись свежим Watch в статусе `watching`. Блок и связь с инцидентом теряются без вызова `blocked:resolved`.

### 5. Бюджет rebase не применяется, бюджет wake сжигается быстрее и паркует Watch навсегда

`Policy.MaxRebases` пишется при регистрации (`tool_watch_pr.go:446-455`) и хранится в Watch, но ни один продакшен-путь его не читает. Поиск по `internal` и `cmd` показывает только конфигурацию и тесты. ADR 0259:15 это подтверждает прямо: поле пока не управляет блокировкой. В мосте есть только `claimRebase`, одна попытка на пару `(watchID, revision)` в памяти (`bridge.go:379-401` на разделённой ветке). После рестарта процесс забывает эту пометку.

Счётчик wake живёт в обязательстве `wakes:<watchID>` и увеличивается на каждую успешную доставку (`wake_dispatcher.go:71-99`, `:242-328`). Счётчик общий для всех подписчиков Watch. Когда `EvaluatePR` возвращает targets для executor и orchestrator, один цикл тратит две единицы. При дублировании подписчиков тратится больше. Предел в шесть доставок (`domain/types.go:114-137`) достигается за три цикла. `parkForWakeCap` ставит `StatusParked` и пишет строку в ESCALATIONS.md (`wake_dispatcher.go:340-389`). Запись в `ESCALATIONS.md` от 2026-11-20 подтверждает сценарий: Watch израсходовал 6 wake без зелёного CI и больше не будится. `parked` не входит в `ActiveWatches`, оркестратор его не видит. `applyVerdict` игнорирует parked (`verdict_applier.go:86-89`). Единственный выход из parked, который реален на практике, это смена head через `handleHeadChanged`.

## Разбор edge cases

### `blocked:not_mine` против `blocked:resolved`

Обе ветки идут через `ackReceiptWithSession` (`tool_ack.go:509-592`). Проверка ревизии стоит до switch и отклоняет только `in.Revision < committed` (`:529-532`). Нулевая ревизия обходит проверку, а ревизия из будущего принимается. ADR 0155:17 требует равенства текущей ревизии и вердикта `retry`. Код вердикт не проверяет вовсе.

`applyBlockedNotMine` (:626-708) берёт упавшие проверки из Watch, а не из аргументов вызова, что правильно. Session ID берётся из `_meta`, а не от клиента. С подключённым blockers port инцидент дедуплируется по `(repo, baseRef, signature)`, где signature это хеш отсортированных имён проверок (`internal/infra/store/blockers.go:69-78`, `:153-207`). Два Watch с разными наборами упавших проверок дают два инцидента. `blocked:resolved` снимает только один инцидент (:710-752). Второй Watch остаётся заблокированным, пока `ResolveBlockersByBaseGreen` не закроет его по зелёному main (:289-355). Это работает, только если провайдер реально наблюдал проверки базы. На разделённой ветке `applyMerged` проверяет `obs.base.ChecksKnown` перед вызовом (`verdict_applier.go:361-369`), на `main` такой защиты нет.

Ещё одна развилка: без blockers port `applyBlockedResolved` применяет `UnblockEvent` только к одному Watch (:754-773), а с port снимает все Watch инцидента одной транзакцией. Поведение зависит от того, подключён ли порт в composition root. Это легитимная миграционная развилка, но она делает результат `beacon_ack` непредсказуемым для агента.

### Дубли подписчиков на один PR

`Fanout.Subscribe` не добавляет подписчика повторно, если `hasSubscriber` уже нашёл его ID (`fanout.go:115-123`, `:231-238`). Предел в 50 подписчиков на Watch есть. На этом уровне дублей нет.

На уровне доставки дубли возможны. `publishActionable` вызывает `wakeOne` для каждого подписчика с нужной ролью (`verdict_applier.go:144-160`). `wakeAddressee` проверяет, что ID подписчика начинается с `ses_` и что он есть на Watch (`wake_dispatcher.go:689-710`). Роль `orchestrator` при регистрации не обязана иметь `ses_`-ID (`tool_watch_pr.go:392-398`), поэтому оркестратор, зарегистрированный с cardID, сохраняется, но wake до него не дойдёт. Блокирующий wake идёт мимо `wakeAddressee`: `notifyBlocked` сам выбирает сессию через `blockedOrchestrator` и звонит адаптеру напрямую (`verdict_applier.go:622-649`, `:666-743`). Такой оркестратор получит blocked, но не получит обычный verdict.

Отдельный случай: `episodeActive` в `internal/infra/ladder/escalation.go:190-201` считает эпизод активным, если любой подписчик Watch показывал активность за 15 минут. Один активный оркестратор подавляет эскалацию молчаливого исполнителя.

### Stale Watch после force-push

Путь для живого Watch работает: `Refresh` видит новый SHA, `handleHeadChanged` закрывает старый Watch с `closeReason=head_changed` и создаёт преемника с теми же подписчиками и обязательствами (`verdict_applier.go:19-82`). Преемник получает детерминированный ID `watch:<repo>:<pr>:<head>`. Если такой Watch уже есть, код пишет `head_successor_exists` и уходит, оставляя старый закрытым (`verdict_applier.go:40-44`). Восстановления после частичной записи нет.

Три случая, когда путь не работает. Watch в `blocked`. Watch в `parked` без наблюдения CI. Терминальное состояние PR, потому что проверка `!isTerminalState` в `Refresh` отключает смену head для MERGED и CLOSED. Последнее разумно, первые два оставляют пробелы.

### Rebase budget

Config и `Watch.Policy` несут `MaxRebases`, но применения этому полю нет. Авто-rebase в `applyMerge` делает одну попытку на ревизию через `claimRebase` (`verdict_applier.go:265-338`). Попытка не пишется на диск. После рестарта счётчик обнуляется. Вердикт `rebase` уходит исполнителю через wake с командой из `wakeText`. Повторных отказов достаточно, чтобы Watch зациклился.

### Wake budget

Счётчик общий для Watch, а не для подписчика. `wakeCount` читает длину `Attempts` обязательства-счётчика (`wake_dispatcher.go:77-84`). Транспортные ошибки счётчик не трогают: `recordWake` вызывается только после успешной доставки (`:321-323`). Мёртвая сессия тоже не списывает бюджет, но помечает подписчика `orphaned` и уходит в `markOrphaned` (`:307-309`, `:391-458`). `publishActionable` не проверяет фазу подписчика, поэтому orphaned-подписчик продолжает получать wake на каждой новой ревизии. Circuit breaker по адаптеру в памяти (`wake_dispatcher.go:101-235`) при OPEN молча пропускает доставку, не ставя обязательство в очередь и не эскалируя. После рестарта breaker закрыт.

### Deadline

`DeadlineMs` при регистрации превращается в `DeadlineAt` (`tool_watch_pr.go:459-461`). `snapshotFor` копирует его в снапшот, `EvaluatePR` возвращает `VerdictTimeout`, `transitionVerdict` ставит `StatusNotifying` и wake оркестратору. Повтор того же вердикта не меняет ревизию и не шлёт второй wake (`internal/domain/transition.go:402-411`). Для `StatusParked` и `StatusBlocked` вердикт не применяется, поэтому дедлайн на таких Watch не сработает, даже если поле заполнено.

### `required: false` и `keep_working`

См. находку 1. Это прямое противоречие между AGENTS.md:113 и `checkFacts`. Система не различает обязательную и информационную проверку на входе в движок вердиктов.

## Примеры из сессии

### PR #1112, blocked

Описание: Watch получил `blocked:not_mine`. Что сработало хорошо. Ветка `blocked:` в `beacon_ack` отделена от `approve/reject` и не возобновляет агента. Имя блокировщика берётся из `_meta`, а не из входных данных. Повторный ack идемпотентен: `GetOrCreateOpenBlocker` не добавляет дубль в `BlockedWatchIDs`, а `BlockedRegistry.Block` сохраняет первую запись. `Refresh` не шлёт retry для blocked Watch, а только маршрутизирует оркестратору.

Что не сработало. Если #1112 получил `blocked` по информационной проверке, причина та же, что в находке 1: движок сам не различает required. Если фиксер по #1112 зарегистрировался и его PR не влился, счётчик в Watch остался нулевым, и оркестратор будет просить следующего фиксера без перехода к человеку (находка 2). Если executor после блокировки вызвал `beacon_watch_pr` повторно на том же SHA, блок мог быть перезаписан (находка 4).

### PR #1111, keep_working

Описание: Watch остаётся в `keep_working`. Что сработало хорошо. Сам вердикт и текст wake несут конкретное действие: починить перечисленные `failedChecks` и не делать пустой rebase. Повтор того же вердикта не меняет ревизию и не шлёт новый wake, поэтому цикл сообщений не растёт (`transition.go:408-411`).

Что не сработало. `keep_working` не различает обязательную и информационную проверку. Если красная проверка на #1111 была `required: false`, агент получил задание чинить то, что чинить не нужно. Вторая проблема: `applyVerdict` кладёт свежие `Checks` и `Merge` в локальную копию Watch, но `publishActionable` при неизменившемся вердикте возвращается без `SaveWatch` (`verdict_applier.go:95-99`, `:124-128`). `beacon_get_result` продолжает показывать старый срез проверок. Третья: watch-счётчик тратится на каждую доставку, поэтому серия `keep_working` с несколькими ролями быстро паркует Watch.

### PR #1105, stale head

Описание: Watch с устаревшим head. Что сработало хорошо. Для живого Watch смена head детерминированно закрывает старую запись и создаёт преемника с подписчиками и обязательствами. `closeSuperseded` в `beacon_watch_pr` делает то же на пути регистрации. Оба пути используют `domain.HeadChangedEvent`, поэтому `closeReason` и `ClosedAt` согласованы.

Что не сработало. Если на момент смены head Watch был в `blocked`, мост его не тронул из-за порядка проверок в `Refresh`. Если executor перерегистрировался на новый SHA, старый blocked Watch не был закрыт, потому что `closeSuperseded` читает только active. Преемник не наследует `DeadlineAt` и `ConfigID`. Если запись преемника уже существовала, код остановился на `head_successor_exists` и не восстановил обязательства.

## Таблица проблем и исправлений

Приоритет P0 означает потерю корректности в текущей сессии, P1 системный риск, P2 улучшение наблюдаемости и чистоты кода.

| # | Проблема | Доказательство | Исправление | Приоритет |
| --- | --- | --- | --- | --- |
| 1 | `required: false` даёт `keep_working` и wake исполнителю | `bridge.go:608-629`, `verdict.go:205-213`, AGENTS.md:113 | Фильтровать необязательные проверки в `checkFacts` или передать `Required` в `Snapshot` и учесть в `realFailures`. Добавить тестовый кейс с упавшим informational-гейтом | P0 |
| 2 | Счётчик FIX-агентов в Watch никогда не растёт, `escalate_human` недостижим | `decision.go:226-236`, `orchestrator.go:399-419`, `blockers.go:209-231`, `cmd/beacon/orchestrate.go:139-149` | Вызывать `RecordFixAttempt` при подтверждённом спавне фиксера или читать счётчик инцидента в `projectBlocked`. Один источник истины | P0 |
| 3 | Заблокированный Watch переживает force-push и может быть перезаписан регистрацией | `bridge.go:539-559`, `tool_watch_pr.go:679-711`, `fanout.go:92-113`, `db.go:386-392` | Проверять смену head до ветки blocked. В `closeSuperseded` и `Fanout` искать алиас в `LiveWatches`, а не только в active. Запрещать перезапись blocked Watch без `blocked:resolved` | P0 |
| 4 | `NoChecksTimeout` всегда ноль, `NoChecksSince` не пишется, дедлайн теряется на преемнике | `verdict.go:52-54`, `:315-318`, `types.go:262-263`, `bridge.go:640-659`, `:694-712` | Добавить поле timeout в Watch и конфиг, заполнять его в `snapshotFor`, копировать `DeadlineAt` и `NoChecksSince` в преемника | P1 |
| 5 | `MaxRebases` не применяется, попытка авто-rebase не durable | `tool_watch_pr.go:446-455`, `bridge.go:379-401`, ADR 0259:15 | Списать ребаз в обязательство Watch по ключу `(watchID, revision)` и сравнить с `MaxRebases` перед выдачей verdict `rebase`. При исчерпании ставить blocked в общий инцидент | P1 |
| 6 | Wake-бюджет общий на всех подписчиков и уходит в parked без выхода | `wake_dispatcher.go:71-99`, `:242-328`, `:340-389`, ESCALATIONS.md:3 | Считать wake по паре `(watchID, subscriberID)` или не тратить бюджет на orchestrator. Перед парковкой писать структурированную эскалацию с watchID и инцидентом, а не только строку в файл | P1 |
| 7 | Orphaned подписчик продолжает получать wake | `wake_dispatcher.go:307-309`, `:391-458`, `verdict_applier.go:144-160` | Проверять `WorkerPhaseOrphaned` в `publishActionable` до вызова `wakeOne` | P1 |
| 8 | Circuit breaker молча теряет wake, состояние в памяти | `wake_dispatcher.go:155-235`, `:257-261` | При OPEN ставить отложенное обязательство или писать метрику с watchID и adapter. Перенести счётчик отказов в store, если потеря wake после рестарта недопустима | P1 |
| 9 | Проверка ревизии в `beacon_ack` пропускает 0 и будущие значения | `tool_ack.go:529-532`, ADR 0155:17 | Требовать `Revision != 0` для blocked-решений и отклонять `Revision != committed`, а не только меньшее | P1 |
| 10 | `blocked:resolved` снимает инцидент, поведение зависит от порта | `tool_ack.go:710-774`, `blockers.go:233-273` | Всегда требовать blockers port в composition root. В receipts возвращать список снятых Watch, а не только булево значение | P2 |
| 11 | `episodeActive` подавляет эскалацию из-за активности другого подписчика | `escalation.go:190-201` | Считать активность только подписчика, владеющего episode obligation | P2 |
| 12 | Преемник после head change не восстанавливает обязательства, если уже существует | `verdict_applier.go:40-44` | При `head_successor_exists` проверять обязательства преемника и дозаписывать недостающие | P2 |
| 13 | Документация расходится с кодом по blocked-контракту | ADR 0155:17 требует `blockReason` и вердикт `retry`, в `AckInput` их нет | Обновить ADR под текущий контракт или добавить проверку вердикта в `applyBlockedNotMine` | P2 |

## Что не проверялось

Живой прогон моста, SQLite и MCP-сервера не выполнялись. Эксплуатационные логи Beacon и транскрипт сессии в репозитории отсутствуют, поэтому примеры #1112, #1111 и #1105 разобраны по коду и по описанию симптомов. Разделённая ветка `fix/ardh11-split-bridge` содержит дополнительные поля `BaseHeadSHA` и `BaseChecksKnown` и вердикт `recheck`, которых нет на `main`. Все выводы, перенесённые с разделённой ветки на `main`, дополнительно проверены по `bridge.go` основной ветки.

# Beacon: словарь домена

Словарь домена цикла Beacon. Здесь зафиксировано, кто работает над карточкой, кто наблюдает за PR, как executor выходит из работы в ожидание и как оркестратор разбирает блокеры main. Словарь описывает домен, а не код: пути, тайминги и номера ADR собраны в разделе «Ссылки на реализацию».

## Порог ADR

ADR стоит предлагать только когда выполнены все три условия: решение трудно обратить, оно удивительно без контекста и оно стало результатом реального компромисса. Если не выполняется хотя бы одно условие, решение остаётся в глоссарии без ADR.

## Language

### Роли

**Executor**:
Агент, которому принадлежит карточка и PR и который отвечает за результат своей работы. Регистрирует Watch с `role: "executor"`.
_Avoid_: worker, coder, subagent

**Orchestrator**:
Тонкая сессия, которая читает вердикты Watch, ждёт зависимости по `waitFor` и спавнит FIX-агента, когда падение проверки относится к main. Оркестратор не чинит main сам.
_Avoid_: coordinator, менеджер задач, main

**RoleFixer**:
Роль подписчика, который чинит заблокированный Watch. Регистрация требует `fixFor` с идентификатором Watch в статусе `blocked`. Попытка списывается на `BlockerIncident` и называется `fixAttempts`.
_Avoid_: repair agent, helper, санитар

**Beacon**:
Инструмент наблюдения и доставки указателей просыпания. Beacon не чинит main и не подменяет агента.
_Avoid_: daemon as actor, controller, automated fixer

### Ядро домена

**Watch**:
Единица наблюдения за PR. Хранит `repo`, `pr`, `headSHA`, `status`, `verdict`, `revision`, подписчиков, обязательства, проверки и политики. Живёт до терминального вердикта, отмены, смены headSHA или успешной команды принятия. Идентификатор имеет форму `watch:<repo>:<pr>:<headSHA>`.
_Avoid_: монитор, отслеживание, PR check

**Subscriber**:
Сторона, ждущая результата Watch. Поля: `id`, `role` (`executor`, `orchestrator`, `fixer`), `adapter`, `target`, `phase`. adapter и target задают адрес доставки wake, роль задаёт, кого будить по вердикту.
_Avoid_: listener, подписка, consumer

**WorkerPhase**:
Фаза жизненного цикла воркера, привязанного к Watch. Значения: `waiting`, `in_work`, `blocked`, `orphaned`, `finished`. Хранится на подписчике и переживает рестарт процесса. Незаполненный подписчик никогда не считается живой работой.
_Avoid_: state, статус сессии, presence

**Verdict**:
Результат, посчитанный для одной ревизии Watch. Значения: `watching`, `ready`, `rebase`, `retry`, `keep_working`, `timeout`, `merged`, `closed`, `blocked`, `recheck`. Переходы добавляют `needs_input`, `parked` и `accepted`. Оркестратор добавляет `fail`, `wait`, `wait_dependency` и `green_blocked`. Вердикт `green_blocked` означает зелёные проверки при удержанном слоте из-за открытого блокера. Вердикт берётся только из `beacon_get_result`, а не из отчёта сабагента.
_Avoid_: status, conclusion, результат агента

**Obligation**:
Одно обязательство подписчика на ревизии Watch. Идентификатор выводится из `watchID`, `revision` и `subscriberID`. Хранит `readAt`, `claimedAt`, `deliveredAt` и историю попыток. `beacon_get_result` ставит `readAt`, `beacon_ack` ставит `claimedAt`.
_Avoid_: task, job, квитанция

**Ladder**:
Лестница пробуждения. Ветвится по исходу доставки: `posted` ждёт прочтения без повторов, `transport-failed` повторяет указатель, `indeterminate` не повторяется никогда. После staging наступает проверка доказательств и эскалация. Позиция сохраняется, поэтому рестарт продолжает с того же шага.
_Avoid_: retry loop, backoff, cron

**ReconcileLoop**:
Периодический проход, который читает живое состояние, считает вердикт и применяет побочные действия. Каждый проход получает собственный `cycleID`, который попадает во все его события.
_Avoid_: polling, heartbeat, watchdog

### Информационные проверки

**Информационная проверка**:
CI-проверка, которая не может заблокировать Watch. Провайдер помечает её как `required: false` в снимке CI, а workflow может задать `continue-on-error: true`. Падение такой проверки не даёт `VerdictBlocked`, `keep_working` и решение `blocked:not_mine`. Её имя не попадает в `failedChecks` в wake, в `BlockedInfo.FailedChecks` и в сигнатуру `BlockerIncident`. Fallback `gh run rerun --failed` тоже исключает её.
_Avoid_: optional check, informational gate, мягкая проверка

**required:false**:
Значение обязательности проверки в снимке CI. Домен читает его в `realFailures`, в правиле 5 `EvaluatePR` и при восстановлении rollup. Проверка с `required: false` не считается провалом при расчёте вердикта и не будит executor. Провайдер обязан брать значение из источника обязательности. Батч-провайдер читает `IsRequired`, легаси-провайдер не должен ставить `true` для всех проверок.
_Avoid_: optional, non-blocking flag, мягкий гейт

**Источники `VerdictBlocked`**:
Проверка, упавшая на базовой ветке и не являющаяся информационной, даёт блок. Исчерпание бюджета ребейза тоже даёт блок, пишет `BlockedInfo` и открывает `BlockerIncident`. Источники вне проверок не зависят от `Required`. Информационная проверка исключена только правилом базовой ветки.
_Avoid_: blocked by check, красная проверка, external failure

**Источники `keep_working`**:
Провал rollup `FAILURE` или `ERROR` без флейковых проверок, ошибка или удержание мержа, когда причина не конфликт после повторной попытки. Источники вне проверок не зависят от `Required`: исчерпание бюджета ребейза и ошибка мержа. Информационная проверка не даёт `keep_working` ни в одном из этих путей.
_Avoid_: failed check, retry forever, жди CI

### Блокеры и управление

**BlockerIncident**:
Запись control plane о проверке, упавшей на базовой ветке. Поля: `repo`, `baseRef`, `signature`, `failedChecks`, `blockedWatchIDs`, `fixerWatchID`, `fixAttempts`, `maxFixAttempts`, `status`. Инцидент ищется по `(repo, baseRef, signature)`, поэтому несколько Watch делят одну запись. `maxFixAttempts` равен трём, после трёх попыток Watch эскалируется к человеку.
_Avoid_: failed check, красный CI, blocker task

**BlockerStore**:
Порт, который хранит `BlockerIncident` и `OrchestratorBinding`. Методы: `SaveBlocker`, `FindBlocker`, `OpenBlockers`, `ResolveBlocker`, `SaveBinding`, `Binding`, `BindingBySession`. Псевдоним `ControlPlaneStore` указывает на тот же порт.
_Avoid_: issue tracker, blockade registry, control plane

**OrchestratorBinding**:
Запись о том, какая сессия сейчас держит роль оркестратора проекта. Поля: `projectKey`, `sessionID`, `adapter`, `boundAt`. Создаётся командой `beacon orchestrator bind`, живёт в `BlockerStore` и переживает рестарт процесса.
_Avoid_: parent session, owner, привязка карточки

**blocked:not_mine**:
Решение `beacon_ack`, которым executor сообщает, что упавшая проверка не относится к его PR. Watch переходит в `blocked` и пишет `BlockedInfo`, оркестратор спавнит FIX-агента. main не чинится.
_Avoid_: чужое падение, external failure, retry

**blocked:resolved**:
Решение `beacon_ack`, которое снимает подтверждённый блок с Watch. Обычно приходит после того, как PR FIX-агента залит в main.
_Avoid_: unblock all, force ready, ignore failure

**orchestrator id**:
Непрозрачный идентификатор родительской сессии, который executor передаёт при регистрации. Хранится на Watch и подписчике, меняется командой `beacon orchestrator bind`. Для blocked wake сначала берётся подписчик с ролью `orchestrator`, при его отсутствии сам идентификатор Watch, и наконец конфигурационный fallback.
_Avoid_: wake target, parentID, owner

**rebind**:
Устаревшая команда `beacon orchestrator rebind --from ... --to ...`. Меняет идентификатор оркестратора у затронутых Watch и отправляет одному новому оркестратору сводное сообщение со списком Watch. Заменена командой `beacon orchestrator bind`.
_Avoid_: перепривязка, reassignment, reparenting

**slot**:
Ёмкость оркестратора по числу Watch, которые он ведёт одновременно. Значение по умолчанию `DefaultSlotsTotal` равно четырём. Слот освобождается при `Unregister` и при вердикте `green_blocked`.
_Avoid_: concurrency limit, лимит параллельности, worker pool

### Состояния executor

**В работе (Working)**:
Executor активно ведёт карточку: меняет код, пушит ветку, открывает или обновляет PR. Этому состоянию соответствует `WorkerPhaseInWork`.
_Avoid_: active, running, in flight

**Жду (Waiting)**:
PR сдан, мониторинг активен, executor спит до вердикта, который разрешает пробуждение. Этому состоянию соответствует `WorkerPhaseWaiting` и фраза `waiting, watchID`.
_Avoid_: idle, parked, watching

**Закончил (Completed, Detached)**:
PR залит, executor отвязан от мониторинга и больше ничего не требуется. Этому состоянию соответствует `WorkerPhaseFinished`. Запись остаётся видимой: переход навсегда повышает observability.
_Avoid_: done, finished, closed

### Наблюдение и сигналы

**Wake**:
Указатель просыпания, который доставляется подписчику при изменении состояния Watch. Несёт только `watchID`, `obligationID`, `revision` и текст инструкции, никогда полный результат проверки.
_Avoid_: notification, уведомление, prompt

### Экран и оператор

**One Screen**:
Целевой интерактивный экран Beacon в одном терминале. Объединяет живой снимок, очереди todo и deferred, события, слоты, воркеров, PR и вердикты, блокеры и RSS в одном окне и принимает клавиши оператора. Отличается от статического board: board рендерит фиксированный список карточек, а One Screen строится по живому снимку.
_Avoid_: dashboard, статический board, веб-интерфейс

**Live Projection**:
Живой снимок `/api/v1/snapshot` плюс поток `/api/v1/events`, спроектированные в экран. Экран пересобирает вид по смене revision, а не по таймеру. Противоположность статической проекции board по списку карточек.
_Avoid_: polling, SSE-клиент сам по себе, снимок БД

**Operator Port**:
Тонкий порт в `cmd/beacon`, через который оператор читает снимок, подписывается на события и отправляет решение. Три метода: читать снимок, подписываться на события, отправлять решение. Порт пропускает действия оператора только через verbs сервера.
_Avoid_: HTTP-клиент в cmd, прямой tui.Run из cmd, hotkey без verbs

**Hotkey**:
Клавиша экрана, привязанная к одному из verbs `/api/v1/human/decide`. Сервер принимает ровно восемь: `approve`, `reject`, `context`, `rework`, `pause`, `resume`, `unblock`, `cancel`. Своих verbs у hotkey нет: spawn и kill не входят в контракт. Отказ сервера остаётся в строке статуса и не меняет локальное представление.
_Avoid_: spawn/kill, алиас, локальный verb

## Ссылки на реализацию

Пути кода, тайминги, номера ADR и ссылки на исследования собраны здесь, чтобы определения выше оставались указателем на домен, а не копией кода.

- Verdict: контракт вердиктов, `internal/domain/types.go`, функция `Verdicts`.
- Ladder: ADR 0017, `internal/infra/ladder`; повтор через 5 и 10 минут с джиттером плюс минус 30 секунд, лимит три попытки, проверка доказательств и эскалация через 15 минут после staging, журнал `ladder_journal`.
- ReconcileLoop: `cibridge.Refresh`, тик опроса CI по умолчанию раз в 30 секунд; проход `beacon daemon` по умолчанию раз в три минуты.
- Источники `VerdictBlocked`: правило 5 в `EvaluatePR`, `internal/domain/verdict.go`; `blockRebaseExhausted`, `internal/infra/cibridge/bridge.go`; `applyBlocked`, `internal/infra/cibridge/verdict_applier.go`.
- Источники `keep_working`: `Evaluate` и `EvaluatePR`, `internal/domain/verdict.go`; `applyMerge`, `internal/infra/cibridge/verdict_applier.go`.
- BlockerStore: реализация на SQLite, `internal/infra/store/blockers.go`.
- One Screen: `docs/research/audit-03-tui.md:3`.
- Live Projection: `httpapi.ProjectionClient`, `cmd/beacon/watch.go:499` (`watchHTTPPort`), `cmd/beacon/watch.go:551` (`renderHTTPSnapshot`), `beacon watch --live`.
- Operator Port: `docs/research/audit-03-tui.md:60`; `net/http` и Bubble Tea вне `cmd`, конкретная программа в `internal/tui`.
- Hotkey: `internal/infra/httpapi/server.go:989`; TUI-10 помечена dropped.

# Исследование: какие инструменты не хватает агентам Beacon

Дата: 2026-11-22. Режим: чтение и запись только документов. Код, тесты, сборки и гейты не запускались. База: `origin/main` `2d34645b`.

Источники: чтение файлов в `internal/infra/mcp/`, `internal/infra/cibridge/`, `internal/app/orchestrator/`, `cmd/beacon/`, `docs/templates/`, `docs/research/`, `docs/tasks/`, `tools/beacon-gates/cardindex/`, `evals/`. Поиск шёл через `rg` с явными каталогами. Номера строк сняты на базе `2d34645b`.

Вопрос владельца: «может нужно еще тулзы агентам присобачить». Три конкретные жалобы: «бикон их не мерджит», `obligation not found`, «required:false тишина». Ниже отдельно указано, где действительно не хватает инструмента, а где политика или уже существующий код.

## 1. Что умеют десять advertised-инструментов

Advertised-набор задан таблицей `toolDescriptors` в `internal/infra/mcp/server.go:146-249`. Отдельно регистрируется только `beacon_wake` в композиционном корне. Контрактный тест фиксирует десять имён, 19680 байт и SHA `1a0bf147…` (`internal/infra/mcp/contract_test.go:27-38`).

| Инструмент | Дескриптор | Вход | Что даёт | Чего не даёт |
| --- | --- | --- | --- | --- |
| `beacon_watch_pr` | `server.go:146-157` | `WatchPRInput`, `tool_watch_pr.go:219-233` | Регистрация Watch на head PR, подписка, политика мержа, лимиты, `fixFor` | Не читает состояние PR и проверки без регистрации Watch |
| `beacon_wait_result` | `server.go:158-166` | `WaitResultInput`, `tool_wait_result.go:304` | Ограниченное long-poll ожидание, вердикт, причина останова | Не читает логи и не меняет CI |
| `beacon_get_result` | `server.go:167-177` | `GetResultInput`, `tool_get_result.go:39-42` | Вердикт, `failedChecks`, `nextAction`, `nextCommand`, `failureExcerpt` до 2 КБ | Не отдаёт полный лог, не показывает чужие обязательства, не объясняет `required:false` |
| `beacon_list_running` | `server.go:178-186` | `ListRunningInput`, `tool_list_running.go:254` | Снимок всех Watch, включая `MergePolicyOutput` (`schema.go:298-303`, `:351`) | Не показывает причину удержания мержа и не ищет по PR |
| `beacon_ack` | `server.go:187-203` | `AckInput`, `tool_ack.go:114-142` | Штамп `claimedAt`, решения `approve`, `reject`, `blocked:not_mine`, `blocked:resolved` | Не пишет вердикт и не мержит |
| `beacon_cancel_watch` | `server.go:204-212` | `CancelWatchInput`, `tool_cancel_watch.go:36` | Закрытие Watch или одного подписчика | Не закрывает PR и не удаляет ветку |
| `beacon_stats` | `server.go:213-221` | `StatsInput`, `tool_stats.go:255` | Счётчики состояния, блокеры, лестница, реестр | Не объясняет конкретный Watch |
| `beacon_defer_wiring` | `server.go:222-230` | `DeferWiringInput`, `tool_defer_wiring.go:32` | Карточка `WIRING-<PR>` для неподключённой проводки | Не работает с PR и CI |
| `beacon_task_claim` | `server.go:231-239` | `TaskClaimInput`, `tool_task_claim.go:41` | Атомарная выдача карточки | Не знает про PR |
| `beacon_task_close` | `server.go:240-248` | `TaskCloseInput`, `tool_task_close.go:43` | Закрытие карточки с причиной | Не знает про PR |

Вывод: все десять инструментов работают с состоянием Beacon, карточкими и обязательствами. Ни один не читает GitHub напрямую ради ответа агенту и не выполняет действие в GitHub от имени агента.

## 2. Ручные операции агентов и покрытие MCP

| Операция | Где встречается | Покрытие MCP |
| --- | --- | --- |
| `gh pr view` | `internal/infra/ghci/provider.go:240-252`, `cmd/beacon/orchestrate.go:192`, `internal/app/orchestrator/store.go:85,224` | Нет read-инструмента. `beacon_watch_pr` получает headSHA через провайдера только при registering Watch. Карточка AT-01 |
| `gh pr checks` | `internal/infra/ghci/provider.go:266-274`, `internal/app/orchestrator/checkall.go:429` | Проверки видны только внутри Watch через `get_result` и `list_running`. Нет запроса по `repo` и `pr` без Watch. Карточка AT-01 |
| `gh run view --log-failed` | `tool_get_result.go:56-58`, `:504-511` | Частично заменено `failureExcerpt`: до 2 КБ, до 8 аннотаций, кэш по `(watchID, headSHA, checkName)`. Полный лог и постраничное чтение отсутствуют. Карточка AT-02 |
| `gh run rerun --failed` | `schema.go:109-110`, `server.go:110`, `tool_get_result.go:418-420`, `docs/templates/executor-task.md:293` | Нет инструмента. По ADR 0004 и шаблону исполнителя rerun выполняет агент своими правами. Карточка AT-06 требует решения владельца |
| `git merge-base --is-ancestor` | `docs/templates/executor-task.md:83,263-272`, `docs/runbooks/CLOSE-PROCEDURE.md:36`, `docs/runbooks/BLOCKER-CLOSE-PLAN.md:43-71` | Нет и не должно быть. Команда читает локальные git-объекты, а MCP-сервер не знает worktree агента. Остаётся локальной проверкой |
| `git rebase` и `git push --force-with-lease` | `schema.go:108`, `tool_get_result.go:415-417`, `bridge.go:574-585`, `verdict_applier.go:440-470` | Нет инструмента. Существует авто-rebase в cibridge. Карточка AT-07 требует решения владельца |
| `gh pr close` | Нет вызовов в advertised-инструментах | Нет инструмента. Карточка AT-08 |
| `gh api branches DELETE` | Нет вызовов в advertised-инструментах | Нет инструмента. Карточка AT-08 |

## 3. Три жалобы, разложенные по коду

### 3.1 «Бикон их не мерджит»

Мерж в системе есть. `verdictApplier.applyVerdict` направляет зелёный вердикт в `applyMerge` (`internal/infra/cibridge/verdict_applier.go:180-186`), а `applyMerge` вызывает `callMerge` и закрывает Watch (`verdict_applier.go:399-494`). Условия, при которых мерж не происходит:

- `autoMerge` выключен или не привязан `Merger`. Сеттер `WithMerger` в `bridge.go:563-572`. Без него готовый вердикт публикуется и будит оркестратора.
- `mergePolicyHold` возвращает причину (`verdict_applier.go:202-226`): `require_approval`, `resolver_unavailable`, `wait_for:<ref>`, `wait_for_main:<ref>`. Watch уходит в `VerdictWaitDependency:<reason>` через `publishMergeWait` (`:228-275`).
- База ушла вперёд после снимка. Тогда `publishRecheck`, а не мерж (`verdict_applier.go:436-438`, `:325-379`).
- Watch запаркован по лимиту wake. Порог считает `wakeCap` (`wake_dispatcher.go:409-417`), парковку пишет `parkForWakeCap` (`:419-430`), вызов идёт из `wakeOne` (`:286-287`).
- Исчерпан бюджет rebase. Счётчик и лимит живут в `bridge.go:594-665`, значение по умолчанию 3 (`:74-76`).

Агент видит только `verdict=ready` и подсказку «the orchestrator merges, no command is needed here» (`tool_get_result.go:424-426`). Причины удержания в `GetResultOutput` нет. `beacon_list_running` показывает только сырые флаги `MergePolicyOutput`, не вычисленную причину. Отсюда ощущение, что бикон не мерджит. Карточка AT-03 добавляет read-инструмент с причиной, не второй путь мержа.

### 3.2 `obligation not found`

`beacon_get_result` строит ключ обязательства из `(watchID, revision, sessionID)` (`tool_get_result.go:457-464`) и падает, если строки нет. `beacon_ack` требует тот же `obligationID` и совпадения `WatchID` (`tool_ack.go:584-597`).

Обязательства создаёт cibridge в пяти местах: `publishMergeWait` (`:257-269`), `publishActionable` (`:300-311`), `publishRecheck` (`:358-368`), `closeWatch` (`:601-611`), `notifyBlocked` (`:823-834`). Если агент вызвал `get_result` на ревизии, для которой его подписчик не получил обязательство, или подошёл с другим sessionID, ответа нет. Списка обязательств по Watch в MCP нет. Готовый шаблон чтения уже существует в `bridge.go:607-625` (`rebaseAttempts` вызывает `WatchObligations`). Карточка AT-04 переносит это чтение в read-инструмент.

### 3.3 «required:false тишина»

Правило зафиксировано в `AGENTS.md`: информационные проверки с `required: false` не являются блокерами. Код следует ему буквально. `shipFactsFromWatch` пропускает проверку, если `!check.Required` (`tool_get_result.go:910-926`). `collectFailedChecks` всё же возвращает упавшие проверки с полем `Required` (`:951-966`), но не говорит, повлияла ли проверка на вердикт.

Агент видит красную проверку в `gh pr checks` и тишину от бикона. Нет инструмента, который показал бы все проверки Watch с флагами `required`, `isFailure`, `countsForVerdict` и причиной. Карточка AT-05 закрывает этот разрыв без изменения движка вердиктов.

## 4. Что уже есть и что не надо дублировать

- **Логи упавших проверок.** `FailureExcerptSource` (`tool_get_result.go:61-67`), кэш (`:111-208`), источник на REST (`newEnvFailureExcerptSource`, `:682-848`), лимит 2 КБ и 8 аннотаций (`:69-84`). AT-02 расширяет этот порт бюджетом и смещением. Второй независимый GitHub-клиент создавать нельзя.
- **Мерж.** `callMerge` и `applyMerge` (`verdict_applier.go:399-494`), порт `domain.Merger` и `WithMerger` (`bridge.go:563-572`). Любой будущий инструмент мержа должен вызвать тот же порт, а не повторять логику.
- **Авто-rebase.** `WithAutoRebase` (`bridge.go:574-585`) и блок в `applyMerge` (`verdict_applier.go:440-470`). AT-07 почти дубль и вынесен на решение владельца.
- **Бюджеты.** RerunBudget считает `remainingRetryBudget` (`tool_get_result.go:995-1022`), flaky-классификация живёт в `internal/domain/verdict.go`, wake cap в `wake_dispatcher.go:409-430`, rebase budget в `bridge.go:594-665`. AT-03 должен читать эти числа, а не вести свои счётчики.
- **Команды nextStep.** `RebaseCommand` и `RetryCommand` определены в `schema.go:105-110`, подсказки в `tool_get_result.go:407-437`. Если владелец отклонит AT-06 и AT-07, эти строки остаются единственным каналом.
- **CLI-диагностика.** `beacon inspect`, `beacon watch`, `beacon board` уже описаны в `docs/templates/executor-task.md:41-47`. Если цель только диагностика, можно расширить CLI вместо MCP. AT-01, AT-03, AT-04 рассчитаны на вызов внутри хода агента.
- **Инструмент вне контракта.** `t3_thread_read` намеренно не входит в `toolDescriptors` (`server.go:794-825`). ADR 0263 запрещает тихую регистрацию. Новые инструменты идут только через дескриптор.

## 5. Предлагаемые инструменты

| Карточка | Инструмент | Тип | Закрывает |
| --- | --- | --- | --- |
| AT-01 | `beacon_pr_status` | read | `gh pr view`, `gh pr checks` без Watch |
| AT-02 | `beacon_ci_failed_log` | read | Полный лог упавшей проверки вместо 2 КБ |
| AT-03 | `beacon_merge_status` | read | «Бикон не мерджит», причина удержания |
| AT-04 | `beacon_obligations` | read | `obligation not found` |
| AT-05 | `beacon_checks_explain` | read | «required:false тишина» |
| AT-06 | `beacon_rerun_failed` | write, под решение владельца | `gh run rerun --failed` |
| AT-07 | `beacon_rebase` | write, под решение владельца | `git rebase` и push с lease |
| AT-08 | `beacon_pr_close_dup` | destructive, под решение владельца | `gh pr close` и `gh api branches DELETE` |

Пять первых инструментов не меняют состояния CI и не требуют смены правил ADR 0004. Три последних требуют явного решения владельца: они переносят на сервер действия, которые сегодня закреплены за агентом или уже делает cibridge.

## 6. Общие точки касания для любого нового инструмента

Один дескриптор в `server.go` обновляет три производные таблицы, но не обновляет ручные константы. Список точек собран по `docs/research/mcp-close-tool-brainstorm.md`, раздел 1, и проверен на базе `2d34645b`.

| Файл и строка | Правка |
| --- | --- |
| `internal/infra/mcp/server.go:146-249` | Новая запись `toolDescriptor` |
| `internal/infra/mcp/server.go:91-114` | Строка «Ten tools» в `CycleInstructions`, `:105` |
| `internal/infra/mcp/server.go:404-493` | Новый порт в `Dependencies`, если инструмент читает GitHub или хранилище |
| `internal/infra/mcp/schema.go:417-465` | Output DTO рядом с существующими |
| `internal/infra/mcp/client.go:290-314` | `remoteToolDescriptions` и `remoteInputSchemaFactories` |
| `internal/infra/mcp/contract_test.go:27,32,38` | Бюджет 20 КБ, размер 19680, SHA `1a0bf147…` по замеру в CI |
| `evals/main.go:47,67` | `frozenContractToolsHash` `ad40be47…`, список `contractTools` |
| `evals/README.md:83,119` | Описания `02_catalog` и `CT01_contract_tools_frozen` |
| `docs/AGENTS.md`, `AGENTS.md` | Перечень десяти инструментов |
| Новый ADR | ADR 0094, 0263, 0268 и 0270 фиксируют текущее число инструментов |

Точки выше общие для всех карточек. Их правит один PR, который владелец сериализует. Карточки AT-01…AT-08 не должны одновременно править одни и те же файлы. OwnerFiles в каждой карточке ограничены новым файлом инструмента и его тестом.

## 7. Риски и решения владельца

1. Рост `tools/list`. Один инструмент добавляет примерно 1500 до 2500 байт по историческим замерам в `mcp-contract-drift-audit.md`. Бюджет 20480 байт может быть превышен при нескольких новых именах. Замер берётся в CI, локально не оценивается.
2. Конфликт с ADR 0004. Beacon объявлен read-only по отношению к CI. AT-06 и AT-07 нарушают это правило, если владелец не примет новый ADR.
3. Дублирование cibridge. AT-07 совпадает с авто-rebase, AT-06 совпадает с бюджетом flaky. Карточки описаны так, чтобы повторной логики не возникало, но решение остаётся за владельцем.
4. Деструктивные операции. AT-08 closes PR и удаляет ветку. Без `dry-run` по умолчанию и проверки связанного Watch операция необратима.
5. Формат карточек. `AT-01`…`AT-08` проходят шаблон `cardindex` (`tools/beacon-gates/cardindex/check.go:41`, вариант `[A-Z][A-Z0-9]+-[A-Z0-9]+`). Файл `docs/ROADMAP_STATUS.md` в базе отсутствует, поэтому проверка индекса возвращает зелёный без сравнения.

## 8. Что не проверялось

Тесты, сборка, гейты, `beacon index build`, CLI Beacon и ручной merge не запускались. Обращения к GitHub из этого исследования не делались. Все числа и номера строк сняты чтением файлов на базе `2d34645b`. PR с этим документом регистрируется через `beacon_watch_pr` с `autoMerge: true`, решение о мерже принимает Beacon.

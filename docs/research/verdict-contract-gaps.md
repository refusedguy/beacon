# Остатки контракта вердикта и информационные проверки

Дата: 2026-11-21. Режим: только чтение. `go test`, `go build`, гейты и ручной merge не запускались. Поиск шёл через `rg` с явными каталогами `internal`, `cmd`, `.github`, `docs`.

## Что уже закрыто PR #1279

PR #1279 (`efd014fe`, влит 2026-11-21) изменил два места. В `internal/domain/verdict.go:221-229` правило 5 base-blame стало пропускать проверку, если в `s.Checks` она помечена `Required: false`. В `cmd/beacon/mcp.go` подключён `TaskCloseStore`, из-за чего `beacon_task_close` отвечал `PERSIST_FAILED`. В тесте `verdict_test.go:1668` фикстура стала помечать `ci-build` как required.

Больше ничего не менялось. Правило 5 закрыто, остальные пути чтения проверок остались прежними.

## Сводная таблица

| Место | Симптом | Предлагаемое решение | Тест-кейс |
| --- | --- | --- | --- |
| `internal/infra/cibridge/ci_refresh.go:100-128`, `checkFacts` и `realFailedNames` | `Rollup` становится `FAILURE`, а `Failures` получает имена `required:false` проверок. Отсюда же берутся имена для блоков и wake | Собирать fail-факты через одну доменную функцию, которая отбрасывает `!Required` и безвредные заключения | Снимок с `ci-build` FAILURE required и `ci-info` FAILURE optional: `realFailedNames` возвращает только `ci-build` |
| `internal/infra/cibridge/verdict_applier.go:557-597`, `applyBlocked` | При блоке из-за required-проверки на базе в `BlockedInfo.FailedChecks` и в сигнатуру `BlockerIncident` попадает informational-проверка | Использовать канонический список имён. Информационные имена оставить только в диагностике | Watch с двумя упавшими проверками, одна optional: `BlockedInfo.FailedChecks` и ключ инцидента не содержат optional-имя |
| `internal/infra/cibridge/verdict_applier.go:744-775`, `handleBlocked`, и `:603-610`, `blockedNotice` | Уже заблокированный Watch пересоздаёт инцидент по «грязной» сигнатуре. Wake FIX-агента называет информационную проверку | Передавать в `GetOrCreateOpenBlocker` отфильтрованный список. В тексте wake оставлять только блокирующие проверки | Два Watch с одним required-сбоем и разными optional-сбоями получают один `BlockerIncident` |
| `internal/infra/cibridge/wake_dispatcher.go:807-845`, `wakeText` | При `keep_working` executor видит «чини конкретные failedChecks» со всеми именами, включая informational. При `retry` без `RerunChecks` в `gh run rerun --failed` попадает informational-имя | Брать отфильтрованный список для текста и для fallback `RerunChecks` | `wakeText(VerdictKeepWorking, ...)` с required и optional сбоями не содержит optional-имя |
| `internal/domain/verdict.go:236-249` и `:139-150` | Восстановление rollup работает только для `rollup == "FAILURE"`. Снимок с `Rollup == "ERROR"` и только optional-сбоями даёт `keep_working`. Движок `Evaluate` не фильтрует `Required` вообще | Распространить проверку required-фактов на `ERROR`. В `Evaluate` фильтровать informational до строк решения | Снимок с `Rollup: "ERROR"`, одной optional FAILURE и валидными merge-гейтами: вердикт не `keep_working` и executor не будится |
| `internal/app/orchestrator/checkall.go:442-474`, `VerdictFromChecks` и `FailedChecks` | Параллельный движок вердиктов не смотрит на `Required`. Optional FAILURE даёт `CheckVerdictRetry` и публикуется в Watch | Проводить проверки через один контракт через узкий порт. Минимум: фильтровать `Required` в обеих функциях | Список проверок только с optional FAILURE даёт `watching`, `FailedChecks` пуст. С required FAILURE даёт `retry` |
| `internal/infra/ghci/provider.go:283-289` | Легаси-провайдер проставляет `Required: true` каждой проверке из `gh pr checks`. Проверка с `continue-on-error: true` становится блокирующей | Заполнять `Required` из источника обязательности, а не константой. При отсутствии источника оставлять unknown и не давать `ready` | Снимок легаси-провайдера с optional FAILURE не даёт `keep_working` и не будит executor |
| `docs/GLOSSARY.md`, `docs/RULES-CHECK-CONTRACT.md` | Понятия «информационная проверка», `required:false` и `continue-on-error:true` в словаре отсутствуют. Контракт описывает только rules-gate | Добавить определения и перечень всех источников `VerdictBlocked` и `keep_working`, включая неблоковые пути | Тест контракта сверяет перечень вердиктов и источников с кодом |

## Разбор находок

### 1. Общий источник fail-фактов

`realFailures` в `internal/domain/verdict.go:317-341` отбрасывает проверки с `Required: false` и безвредные заключения через `harmlessConclusion`. `realFailedNames` в `internal/infra/cibridge/ci_refresh.go:125-128` делает то же имя, но зовёт `checkFacts`, который не смотрит на `Required`. Получается две функции с одним назначением и разным поведением.

`checkFacts` ещё и формирует `Rollup`. Для optional FAILURE он возвращает `FAILURE`. `snapshotFor` кладёт это в `domain.Snapshot`. `EvaluatePR` чинит rollup в строках 236-249, но только когда `rollup == "FAILURE"` и есть непустая карта проверок. Исправление работает не для всех производителей снимка.

Базовый замер: два определения fail-фактов, из них одно без фильтра `Required`.После объединения должно остаться одно.

### 2. Блок и FIX-агент

PR #1279 закрыл решение rule 5, но неzasнул список имён. `applyBlocked` вызывает `realFailedNames` и пишет результат в `BlockedInfo` и в `GetOrCreateOpenBlocker`. Инцидент ищется по `(repo, baseRef, signature)`, поэтому разный набор informational-имён у двух Watch с одним required-сбоем создаёт два инцидента вместо одного. `blockedNotice` печатает тот же список в wake оркестратору, который спавнит FIX-агента. `handleBlocked` повторяет ту же операцию на каждом цикле заблокированного Watch.

### 3. Текст wake

`wakeText` на `keep_working` перечисляет `failedChecks` от `realFailedNames`. Executor получает указание чинить informational-проверку, хотя та не блокирует merge. Ветка `retry` использует тот же список, если `RerunChecks` пуст. Это расходится с правилом из `executor-task.md:199-211`: информационная проверка не является блокировкой main.

### 4. ERROR rollup и движок Evaluate

`buildRollup` в `internal/infra/github/errors.go:439-464` умеет возвращать `ERROR`. `StatusResult.Apply` (:336) записывает это в снимок. `EvaluatePR` восстанавливает rollup только из `FAILURE`, поэтому `ERROR` с одними informational-сбоями доходит до строки 250 и даёт `keep_working`.

`Evaluate` в `internal/domain/verdict.go:139-150` не фильтрует `Required` вообще. Сейчас он используется в `EvaluateGreenBlocked` (:971) и в тестах, но остаётся частью контракта §4.6. Любой новый вызывающий получит `keep_working` по informational-проверке.

### 5. Дубль движка в app

`VerdictFromChecks` в `internal/app/orchestrator/checkall.go:442-462` повторяет решение EvaluatePR по проверкам, но не знает про `Required`. Слой app не может импортировать domain по ADR 0005, поэтому значения вердиктов уже дублируются в `checkall.go:29-52`. Нужен искусственно узкий порт, который несёт только факт «есть ли блокирующий сбой», а не весь движок.

### 6. Легаси-провайдер

`internal/infra/ghci/provider.go:283-289` ставит `Required: true` безусловно. Батч-провайдер в `internal/infra/github/batch_provider.go:577` берёт `Required` из `IsRequired`, что лучше, но тоже не читает `continue-on-error`. В коде нет ни одного чтения поля `continue-on-error` из workflow. Поиск по `internal` и `cmd` нашёл только `flag.ContinueOnError` в CLI.

### 7. Четыре нормализации заключений

| Функция | Файл | Особенность |
| --- | --- | --- |
| `NormalizeConclusion` | `internal/domain/verdict.go:300-311` | PASS, SUCCESSFUL, SKIPPED, NEUTRAL = SUCCESS; ACTION_REQUIRED = FAILURE; CANCELLED отдельно |
| `checkFacts` | `internal/infra/cibridge/ci_refresh.go:100-121` | STARTUP_FAILURE = FAILURE; нет PASS, SUCCESSFUL, FAIL, ACTION_REQUIRED |
| `failureConclusions`, `isFailedCheck` | `internal/infra/mcp/tool_get_result.go:44-54`, `:936-947` | STARTUP_FAILURE и CANCELLED = FAILURE; ACTION_REQUIRED отсутствует |
| `buildRollup` | `internal/infra/github/errors.go:439-464` | Только состояния CheckState, без aliases |

Одна и та же проверка может быть провалом в одном пути и pending в другом. Это второй источник расхождений между вердиктом и тем, что видит executor в `failedChecks`.

### 8. Документация

`GLOSSARY.md` определяет `Verdict`, `BlockerIncident`, `blocked:not_mine`, но не определяет информационную проверку. `RULES-CHECK-CONTRACT.md` использует `continue-on-error: true` только применительно к rules-gate job и прямо запрещает его там. Про informational CI-проверки в контракте ничего нет. Правило живёт в `AGENTS.md` и `docs/templates/executor-task.md`, то есть вне канонического контракта вердикта.

## Итог

Перечисленные пути не отменяют правило 5. Они работают после него, до него или в параллельном движке. PR #1279 достаточно для самого правила, но недостаточно для контракта целиком. Карточки `ATX-V01..V08` закрывают каждый путь отдельно.

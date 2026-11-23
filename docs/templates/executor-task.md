# Шаблон задания исполнителя

Шаблон задания для агента-исполнителя или фикера в цикле Beacon. Исполнитель регистрирует Watch на реальную сессию, сдаёт PR через ветку и worktree, завершает ход фиксированной фразой и просыпается только когда мерж невозможен. Сон означает завершение хода, а не опрос в цикле. Основа: WORKER-PROMPT §4.7, ADR 0004, ADR 0019, ADR 0021, ADR 0072, ADR 0258.

## Карта жизненного цикла карточки

Карточка Beacon проходит семь фаз. У каждой фазы есть вход, выход, владелец и артефакт. Фаза без артефакта проваливает карточку.

| Фаза | Вход | Выход | Владелец | Артефакт |
| --- | --- | --- | --- | --- |
| explore | карточка и вопрос владельца | найденные факты | исследователь | research-файл |
| plan | research-файл | шаги, зависимости и две формы | исполнитель | PLAN.md с таблицей владельцев |
| improve | готовый план | пробелы и правки плана | оркестратор | запись ревью |
| implement | просмотренный план | изменения в ветке | исполнитель | diff в PR |
| verify | изменения в PR | результат проверок | CI | отчёт проверок |
| commit | каждая правка и исправление линтера | контрольная точка в ветке | исполнитель | хеш коммита в `SessionCommits` рядом с Watch |
| rules-check | изменённые файлы | нарушения правил | gate | отчёт проверки правил |

Карта повторяет фазы Harbor workflow-lifecycle и шесть фаз цикла aif-loop (ADR 0229, ADR 0241). Beacon не переносит код Harbor и использует только перечень фаз.

<!-- executor-task:lifecycle
phases: explore, plan, improve, implement, verify, commit, rules-check
fields: input, output, owner, artifact
no-artifact: fail
executor-task:lifecycle -->

## Форма проекта до кода

Для нетривиального изменения, которое пересекает границу функции, исполнитель готовит две формы проекта до фазы implement. Форма задаётся типами, сигнатурами и границами модулей. Приём взят из `pstack/skills/architect/SKILL.md:9`, где требуется набросать две разные формы до кода.

Две формы попадают в `PLAN.md` на фазе plan. Первая форма описывает типы, сигнатуры и границы. Вторая даёт другую структуру, а не другие имена. Для каждой формы исполнитель заполняет таблицу состояния и владельца.

| Форма | Состояние | Владелец состояния |
| --- | --- | --- |
| A | состояние A1 | модуль X |
| B | состояние B1 | модуль Y |

Владелец состояния один для каждого состояния. Состояние с двумя владельцами или без владельца не годится. Исполнитель выбирает форму с меньшим числом владельцев. Если владельцев поровну, он берёт форму, где вызывающий знает меньше. Выбор и причина записываются в `PLAN.md`.

Сравнение обязательно для фазы plan. `PLAN.md` без двух форм и таблиц владельцев проваливает фазу plan по правилу `no-artifact: fail`. Правило не действует на правки, которые не пересекают границу функции. Протокол Watch и число MCP-инструментов не меняются. Источник: карточка PSKX-01 и `pstack/skills/architect/SKILL.md:9`.

<!-- executor-task:design-shapes
trigger: non-trivial, crosses-function-boundary
artifact: PLAN.md, phase plan
shapes: two, types-signatures-boundaries
owner-table: state, exactly-one-owner-per-state
select: fewer-owners, then less-caller-knowledge
no-comparison: fail-plan
scope: no-protocol-change
source: pstack/skills/architect/SKILL.md:9, PSKX-01
executor-task:design-shapes -->

## Новое требование как исходное предположение

Когда требование приходит в уже существующий дизайн, исполнитель не приклеивает его к готовой форме. Он перепроектирует затронутую часть так, будто новое требование с самого начала было заложено как исходное предположение. Приём взят из `pstack/skills/principle-redesign-from-first-principles/SKILL.md:9-15`.

Шаг «Исходное предположение» добавляется в фазу plan рядом с двумя формами проекта. Исполнитель выполняет его по порядку.

1. Перечитать все затронутые файлы целиком, а не только места будущей правки.
2. Сформулировать, как выглядела бы система с новым требованием с нуля, без оглядки на текущую форму.
3. Перечислить все места, куда изменение должно распространиться. Список обязательно содержит типы, docs, примеры и обоснование (rationale). Пропущенная категория проваливает фазу plan.

Доставка идёт инкрементально. Временный слой совместимости без срока удаления не принимается: если такой слой нужен, в плане указан коммит или условие его удаления.

Приём не создаёт новый глобальный скилл и не меняет протокол Watch. Он дополняет фазу plan и не заменяет две формы из блока `executor-task:design-shapes`. Приём не дублирует redesign из PSK-01, который действует только внутри протокола кандидатов.

<!-- executor-task:original-assumption
phase: plan
step: исходное предположение
reread: all-affected-files
from-scratch: design-with-requirement-as-original
spread: types, docs, examples, rationale
spread-missing: fail-plan
delivery: incremental
temporary-compatibility: requires-removal-date
scope: no-new-skill, no-watch-protocol-change, not-psk-01
source: pstack/skills/principle-redesign-from-first-principles/SKILL.md:9-15, PSKX-30
executor-task:original-assumption -->

## Входные данные

| Поле | Источник | Назначение |
| --- | --- | --- |
| cardID | ответ `beacon_task_claim` | что делает исполнитель |
| цель, зависимости, OwnerFiles, режим плана, handoff | ответ `beacon_task_claim` | контекст задачи без поиска по `docs/tasks/` |
| пересказ цели | исполнитель на основе цели пользователя | три элемента до шагов плана: цель, проблема, smallest change |
| graphReady | ответ `beacon_task_claim` или `beacon_stats` | граф собран и актуален |
| sessionID | `_meta["ai.opencode/sessionID"]` или явный `notify.target: "<ses_...>"` | адрес живой сессии для wake |
| repo, pr, headSHA, baseRef | факт сдачи PR | параметры Watch |
| watchID | ответ `beacon_watch_pr` | ключ ожидания и отчёта |
| verdict | `beacon_get_result` | решение о пробуждении |
| failureExcerpt, failureExcerptTruncated, failureExcerptLogUrl | `beacon_get_result` | вывод упавшего теста и ссылка на полный лог |
| failedChecks[].failureExcerpt, failureExcerptUnavailable | `beacon_get_result` | вывод и доступность источника для одной проверки |
| tokenBreakdown | `session.TokenBreakdown(ContextSnapshot)` | разбивка контекста по категориям с ценой и остатком |
| tokenWarning | `session.TokenUsage().Warning` | предупреждение при остатке ниже 1024 токенов |
| роль файла контекста | editable из OwnerFiles карточки, readonly из остальных файлов в контексте | какая запись разрешена |
| условие останова | карточка задачи | цель и минимум попыток до останова прогона оптимизации |

### Пересказ цели до фазы plan

Исполнитель пересказывает цель пользователя и решаемую проблему своими словами до фазы plan. Пересказ содержит три элемента: цель, проблема и smallest change. smallest change называет минимальное изменение, которое устраняет проблему.

Пересказ цели попадает в `PLAN.md` до шагов плана. `PLAN.md` без пересказа цели не проходит проверку артефакта фазы plan. Пересказ дополняет цель карточки, а не заменяет её.

Если пользователь не подтвердил пересказ, исполнитель продолжает работу с допущением. Он помечает допущение в `PLAN.md` рядом с пересказом.

<!-- executor-task:restate-goal
placement: PLAN.md, before-steps
elements: goal, problem, smallest-change
unconfirmed: continue-with-marked-assumption
artifact-check: no-restatement-fail-plan
not-replaces-card-goal: true
source: PSKX-09, pstack/skills/in-your-own-words/SKILL.md:7
executor-task:restate-goal -->

## Вычитание перед добавлением

Фаза plan начинается с обязательного подшага «Вычитание». Он идёт до выбора двух форм проекта и до описания новых шагов. Исполнитель перечисляет кандидатов на удаление: мёртвые ссылки, неиспользуемые проверки, заглушки и спекулятивные guard'ы. Для каждого кандидата он приводит proof, что объект не используется: вызывающие стороны, результат поиска по репозиторию или запись в PLAN.md. Затем он удаляет найденное и фиксирует в PLAN.md, что стало проще.

План без списка удаления не принимается. Пустой список допустим, только когда proof показывает, что удалять нечего. Исполнитель не добавляет парсеры, guard'ы и валидаторы сверх требования карточки. Спекулятивная проверка без требования и proof не попадает в план.

Подшаг задаёт порядок «сначала удалить, потом добавить». Он не дублирует PSKX-01 (две формы), PSKX-09 (пересказ цели) и CM-01..05. Протокол Watch и число MCP-инструментов не меняются. Источник: карточка PSKX-31, `pstack/skills/principle-subtract-before-you-add/SKILL.md:9-18`.

<!-- executor-task:subtract-first
phase: plan, first-substep
order: subtract-before-add
step: list-removal-candidates, remove-dead-code, record-simplification
proof-required: per-removed-item
empty-list: allowed-with-proof
forbid: speculative-parsers, guards-beyond-spec
no-list: fail-plan
not-duplicate: PSKX-01, PSKX-09, CM-01..05
scope: no-protocol-change
source: PSKX-31, pstack/skills/principle-subtract-before-you-add/SKILL.md:9-18
executor-task:subtract-first -->

## Учёт токенов контекста

Исполнитель видит, сколько токенов занимает каждая часть его промпта. `ContextSnapshot` хранит счётчики по пяти категориям из `/tokens` в aider (`aider/commands.py:445-532`): `system`, `history`, `repo-map`, `editable`, `read-only`. Категорию задаёт `WithTokenCount`, окно `max_input_tokens` задаёт `WithMaxInputTokens`, цену `input_cost_per_token` задаёт `WithInputCostPerToken`. Все три метода возвращают копию снимка и не меняют полученный.

Метод `TokenUsage` возвращает счётчики, сумму `Total`, остаток `Remaining` и предупреждение `Warning`. Сумма равна сложению всех категорий. Остаток равен `max_input_tokens - total`, ограничен нулю снизу. Предупреждение появляется, когда остаток меньше 1024 токенов, и советует `/drop` или `/clear` (`aider/commands.py:538-551`). Когда окно неизвестно (`max_input_tokens <= 0`), остаток и предупреждение не печатаются.

Диагностическая команда печатает текст из `TokenBreakdown`: по строке на категорию с числом токенов и ценой, затем сумму, окно, остаток и предупреждение. Счётчики копируются при создании и чтении, как и остальные поля снимка. Объявленные MCP-поля формат не меняет.

<!-- executor-task:token-accounting
categories: system, history, repo-map, editable, read-only
api: WithTokenCount, WithMaxInputTokens, WithInputCostPerToken
usage: TokenUsage, TokenBreakdown
total: sum-of-categories
remaining: max_input_tokens-minus-total, clamped-at-zero
warn: below-1024, /drop-or-clear
mcp-fields: unchanged
executor-task:token-accounting -->

| упоминания файлов | текст карточки и ответ исполнителя | пути из текста добавляются в контекст после подтверждения |

## Сжатие истории сообщений

История потомка растёт, а окно контекста ограничено. Правило сжатия заменяет старые наблюдения одной записью и оставляет хвост истории без изменений. Это сохраняет точки кэша из CA-09.

Формат записи: маркер `beacon/compressed` и число опущенных строк `omitted=<N>`. Пример: `beacon/compressed omitted=15`. Число `N` равно количеству заменённых сообщений. Сжатие детерминировано. Одинаковая история и одинаковые параметры дают одинаковую запись.

Тег `keep_output` сохраняет вывод сообщения независимо от возраста. Тег `remove_output` заменяет вывод независимо от возраста, в том числе среди последних наблюдений. Сообщение без тега подчиняется правилу возраста. Первое наблюдение и ключ `beacon/survival-rules` не сжимаются никогда.

Сжатие запускается по шагу `history-compaction-step`, по умолчанию 5 turn. На каждом turn сжатие не запускается. Запуск меняет только голову истории. Хвост из последних `n` наблюдений и ключа `beacon/survival-rules` остаётся байт в байт.

Правило не подменяет CB-05 и CB-06. Те карточки сжимают закрытые карточки на диске. Здесь сжимается живая история сообщений.

Проверка берёт 20 наблюдений. Одно помечено `keep_output`, одно `remove_output`. После сжатия остаются 5 последних наблюдений и первое. Наблюдение с `keep_output` сохранено. Наблюдение с `remove_output` заменено, и строка замены содержит число опущенных строк. Повтор на той же истории даёт тот же результат.

Источник: карточка SB-18, `docs/research/comp-swebench.md`, `sweagent/sweagent/agent/history_processors.py:85-176`.

<!-- executor-task:history-compaction
format: beacon/compressed omitted=N
tags: keep_output, remove_output
protected: first-observation, beacon/survival-rules
step: history-compaction-step, default-5
scope: head-only, tail-byte-identical
deterministic: true
not-duplicate: CB-05, CB-06
acceptance: 20-observations-5-kept-first-preserved-tagged-replaced
source: SB-18, docs/research/comp-swebench.md
executor-task:history-compaction -->

## Выбор модели по фазе

Исполнитель получает модель по фазе. Планировщик работает на модели `Planner`. Исполнитель работает на модели `Editor`, когда она задана. Пустое значение `Editor` оставляет текущий контракт: исполнитель наследует контекст планировщика и использует ту же модель.

При заданной модели `Editor` планировщик передаёт исполнителю только структурированный план и OwnerFiles. История планировщика и его карта репозитория не попадают в контекст исполнителя. План приходит одним сообщением. Функция `ModelFor` выбирает модель фазы, а `PhaseCost.Total()` суммирует стоимость планирования и применения в отчёте.

Проверка берёт 10 задач. Требование: в контексте исполнителя не более одного сообщения плана, истории планировщика нет, суммарная стоимость фаз не превышает стоимость текущего контракта. Успех в 10 из 10 задач. Контракт вердиктов не меняется.

Источник: карточка CA-21, `docs/research/comp-aider.md`, `aider/coders/architect_coder.py:11-47`.

<!-- executor-task:phase-models
phases: plan, execute
types: Phase, PhaseModels, ExecutorBrief, PhaseCost
editor-optional: empty-editor-keeps-current-contract
execute-context: plan-message-and-owner-files-only
no-planner-history: true
cost-report: PhaseCost.Total
verdict-contract: unchanged
acceptance: 10-of-10, at-most-one-plan-message
source: CA-21, docs/research/comp-aider.md
executor-task:phase-models -->

## Упоминания файлов в контексте потомка

Текст карточки и ответ исполнителя содержат имена файлов и идентификаторы от пяти символов. Модель в `internal/app/session/delegation.go` разбирает эти упоминания и сопоставляет их с файлами репозитория по имени без расширения. Идентификатор, который совпадает с несколькими файлами, не добавляется: он неоднозначен.

Файл попадает в `ContextSnapshot` только после подтверждения. Отказ запоминается в `ignored mentions`, и повторный запрос по тому же ключу не приходит. Решение возвращается reflected-сообщением и попадает в `ParentNotice.ReflectedMessage` на следующем ходу.

Источник: карточка CA-10, `aider/coders/base_coder.py:690-707` и `:1761-1781`.

<!-- executor-task:mentions
source: card-text-and-answer
match: basename-without-extension
attach: after-confirmation-only
refusal: remembered, no-repeat
next-turn: reflected-message
executor-task:mentions -->

## Роли файлов контекста: editable и readonly

Каждый файл в контексте исполнителя несёт роль. Роль принимает одно из двух значений: `editable` или `readonly`. Роль лежит в записи контекста рядом с путём. Модель видит все файлы контекста, но запись разрешена только в файлы с ролью `editable`.

Источник ролей фиксирован. Значение `editable` исполнитель берёт из поля «Владелец файлов» своей карточки. Значение `readonly` он ставит на остальные файлы, полученные в контексте. Граница правки по-прежнему задаётся OwnerFiles карточки. Добавление роли не меняет правила владения файлами.

Перед записью исполнитель проверяет роль целевого файла. Файл с ролью `readonly` отклоняет запись с кодом ошибки `readonly_file`. Код входит в текст ошибки, чтобы аудит отличал отказ по роли от других отказов. Файл с ролью `editable` принимает запись без дополнительной проверки.

Смена роли работает без перезапуска сессии. Команда повышения переводит файл из `readonly` в `editable` и принимает путь, который существует в репозитории. Команда понижения возвращает файл в `readonly`. Следующая запись после команды использует новую роль.

Проверка ролей считает 20 попыток записи. Десять попыток идут в `editable`, десять в `readonly`. Все десять записей в `editable` проходят, все десять записей в `readonly` отклоняются с кодом `readonly_file`. Повтор после смены роли подтверждает, что файл доступен без перезапуска. Baseline равен одному нарушению на 12 проверенных PR (PR 1448).

Основание: `docs/research/comp-aider.md`, `docs/research/OWNERFILE-DISCIPLINE.md`.

<!-- executor-task:context-roles
values: editable, readonly
model-sees: all
write-allowed: editable-only
editable-source: card-owner-files
readonly-source: other-context-files
deny-code: readonly_file
promotion: readonly-to-editable, path-must-exist-in-repo, no-session-restart
acceptance: 10-allowed-10-denied, recheck-after-promotion
baseline: 1-violation-per-12-prs
owner-rules: unchanged
source: CA-11, docs/research/comp-aider.md
executor-task:context-roles -->

## Приоритетные файлы перед ранжированными тегами

Контекст потомка начинается с фиксированного списка приоритетных файлов. Список идёт до ранжированных тегов, полученных по частоте упоминаний. aider держит такой список корневых файлов в `aider/special.py:3-177`, отбирает их из всех файлов репозитория в `special.py:184-203` и ставит в начало списка тегов в `aider/repomap.py:656-662`. Список не зависит от частоты упоминаний.

В beacon приоритетные файлы: `README`, `go.mod`, `AGENTS.md` и workflow-файлы `.github/workflows/*.yml`. Без этого списка файлы попадают в контекст только потому, что их читает человек. При нулевом бюджете ranked-теги отсутствуют, и приоритетные файлы теряются.

Исполнитель добавляет приоритетные файлы в начало контекста. Файл, который уже попал в контекст через упоминание или другую категорию, в основной ranked-список не добавляется. Так контекст не содержит дублей. Роль `AGENTS.md` как источника правил не меняется: файл остаётся в приоритетном списке и по-прежнему задаёт правила агента.

Проверка берёт 10 задач с бюджетом 1024 токена. В каждом контексте присутствуют `go.mod`, `AGENTS.md` и один workflow-файл, без дублей. При бюджете 0 присутствует `AGENTS.md`. Baseline равен 0 из 10.

Основание: карточка CA-19, `docs/research/comp-aider.md`, дополнение к CA-01.

<!-- executor-task:priority-files
files: README, go.mod, AGENTS.md, .github/workflows/*.yml
position: before-ranked-tags
frequency-independent: true
dedupe: skip-files-already-in-context
agents-md-role: rules-source, unchanged
acceptance: 10-of-10-at-1024, agents-md-at-zero-budget, no-duplicates
baseline: 0-of-10
source: CA-19, docs/research/comp-aider.md
executor-task:priority-files -->

## Разделение каналов

Агент меняет состояние цикла через MCP-инструменты. Это `beacon_watch_pr`, `beacon_ack`, `beacon_cancel_watch`, `beacon_task_claim`, `beacon_task_close` и `beacon_defer_wiring`. Агент зовёт их внутри хода и читает типизированную ошибку в ответе.

Для диагностики агент запускает CLI-команды `beacon inspect`, `beacon watch` и `beacon board`. Они показывают состояние Watch, карточек и графа. Агент не ищет состояние в логах вручную и не ждёт в bash.

Пробуждение приходит как Wake. Beacon доставляет Wake, агент его не запрашивает. После Wake агент читает результат через `beacon_get_result`. Вердикт из этого ответа определяет следующее действие. Квитанция доставки и текст отчёта вердиктом не являются.

Основание: `docs/research/audit-04-protocols.md`, раздел а.

<!-- executor-task:channels
state-change: mcp
diagnostics: cli (beacon inspect, beacon watch, beacon board)
wake: beacon delivers, agent does not poll
result: beacon_get_result after wake
executor-task:channels -->

## Получение задачи через beacon_task_claim

Задачу выдаёт оркестратор через `beacon_task_claim` с agent ID. Исполнитель получает ID карточки, цель, зависимости, OwnerFiles, режим плана и handoff. Первая выдача помечает карточку `in_progress`, поэтому второй агент не возьмёт ту же карточку.

Инструмент работает в пути `beacon mcp`. Композиционный корень передаёт ему файловое хранилище карточек из `docs/tasks`, поэтому ручной поиск по каталогу не нужен. Если вызов вернул `PERSIST_FAILED`, исполнитель сообщает оркестратору через `beacon_ack` с `decision="blocked:not_mine"` и причиной ошибки. Карточку вручную он не ищет и не открывает.

`docs/tasks/` остаётся источником истины для содержимого карточки, но не единственным путём к задаче. Индекс в state dir пересобирается из карточек командой `beacon index build`. Граф строится из рёбер `blocks`, `depends-on`, `supersedes` и `related` для всех семейств ID: T, RS, WIRING и BS. Цикл на записи отвергается. Не добавляйте зависимость, которая замыкает цикл. Маркер `graphReady` в ответе claim или `beacon_stats` говорит, что граф актуален.

<!-- executor-task:claim
source: beacon_task_claim
fields: id, goal, deps, ownerFiles, planMode, handoff, graphReady
duplicate-issue: fail
cycle: reject
manual-done: forbidden
persist-failed: blocked:not_mine
executor-task:claim -->

## Полный цикл: оркестратор и исполнитель

Оркестратор спавняет N агентов. Каждый агент проходит семь шагов от получения ID карточki до сдачи. Шаг 5 развилки определяет две ветки: работа над новой задачей и закрытие уже сделанной. Обе ветки заканчиваются PR, который доводит до main оркестратор.

| Шаг | Кто | Действие | Артефакт |
| --- | --- | --- | --- |
| 1 | оркестратор | спавнит N сабагентов, одному_signal = один spawn | живые сессии агентов |
| 2 | рантайм | создаёт сабагентов | `ses_...` агентов |
| 3 | оркестратор | передаёт агенту ID карточки из `beacon_task_claim` | cardID в задании |
| 4 | агент | вызывает `beacon_task_claim(agentID)` и получает карточку in_progress | ответ claim |
| 5 | агент | проверяет premise: влита ли карточка в `origin/main` | `git merge-base --is-ancestor` |
| 6a | агент, если НЕ влита | правит код, коммитит, пушит, открывает PR, регистрирует Watch | PR + watchID |
| 6b | агент, если УЖЕ влита | помечает карточкуdone с причиной и доказательством, правит файл карточки, открывает PR | PR с правкой карточки |
| 7 | агент | завершает ход по правилу отчёта и идёт дальше | `waiting, <watchID>` или close-отчёт |

Оркестратор не ждёт ручного пинка владельца. Каждое событие `report_to_orchestrator` заполняет один свободный слот новым спавном. Если main красный по required-проверке, новых сабентов не спавнят до FIX-агента.

### Ветка 6b: карточка уже сделана

Premise проверяется до работы с кодом по правилу «Блокер с доказательством вместо PR-дубля». Если коммит с решением уже в `origin/main`, агент НЕ пишет дубль кода. Вместо этого он выполняет close-ветку:

1. Закрыть карточку через `beacon_task_close` (cardID, status `done`, reason с полным SHA и номером PR). Инструмент идемпотентен по `cardID, status, reason` и проверяет владельца in_progress карточки.
2. Если MCP vernier отвечает `PERSIST_FAILED` (store не подключён),ользоваться CLI `beacon task done <cardID> --summary ... --evidence ...` из корня репозитория. Оба пути пишут `Статус: done`, `Итог`, `Доказательство` и `Причина` в карточку под файловым замком.
3. Сделать PR с правкой карточки в `docs/tasks/`. Это НЕ дубль кода: PR несёт только закрытие задачи и доказательство. Такой PR проходит тот же CI и мержится оркестратором.
4. После закрытия агент готов к следующей карточке и не держит Watch на уже влитом PR.

Правило 6b заменяет прежний запрет «закрывать карточку вручную». Ручным считается произвольное редактирование `Статус:` без доказательства. Закрытие через `beacon_task_close` или `beacon task done` с SHA и номером PR не является ручным: это штатный путь с аудитом.

<!-- executor-task:card-lifecycle
orchestrator: spawn-N, one-signal-one-spawn
steps: spawn, assign-id, claim, check-premise, implement-or-close, pr, finish
branch-6a: not-merged -> code-PR + watch
branch-6b: already-merged -> close-card-with-evidence + card-PR
close-tool: beacon_task_close, fallback beacon task done --evidence
evidence: full-SHA + PR + merge-base-exit-0
duplicate-code-PR: forbidden
executor-task:card-lifecycle -->

## Три состояния исполнителя

Исполнитель находится ровно в одном из трёх состояний из ADR 0258. Переход идёт по порядку: «в работе» сменяется «жду», «жду» сменяется «закончил». Обратного перехода нет.

| Состояние | Действие агента | Отвязка |
| --- | --- | --- |
| в работе | агент правит код в своей ветке и готовит PR | Watch уже зарегистрирован, агент ещё держит задачу |
| жду | агент сдал PR, завершил ход фразой ожидания и спит до вердикта | агент отвязан от ожидания, сон не является опросом |
| закончил | агент прочитал терминальный вердикт, PR залит, требований нет | агент навсегда отвязан от мониторинга, состояние сохраняется |

«Закончил» означает отвязку от мониторинга и залитый PR. Состояние сохраняется и навсегда повышает observability. Оно не совпадает с тихим завершением хода: агент в «жду» вернётся по wake, агент в «закончил» не вернётся.

Исполнитель хранит orchestrator id родительской сессии. rebind меняет только эту связь и пока не маршрутизирует wake.

Beacon остаётся инструментом. Он наблюдает состояние и доставляет wake. Beacon не выполняет работу агента и не чинит чужую поломку.

## Каскад применения структурированных правок

Текущая модель редактирования не меняется: агент правит файлы своего worktree инструментами хоста. Прикладного слоя применения правок в beacon нет. Ниже описан приём на случай централизованной проверки или применения структурированных правок. Он не подменяет инструменты хоста и не требует от агента перехода на другой механизм правки.

Интерфейс каскада задаётся тремя стратегиями и четырьмя вариантами обработки пробелов и отступа. Стратегии по порядку: точная замена, трёхстороннее слияние через временный git-репозиторий, построчный diff-match-patch. Варианты обработки снимают пустые строки и меняют относительный отступ, всего четыре комбинации. Каскад перебирает 3 × 4 = 12 комбинаций и останавливается на первом успехе. Успех означает, что блок search/replace применён к целевому файлу без конфликта.

Относительный отступ кодируется уникальным символом до применения и заменяется абсолютным отступом после. Правка переживает сдвиг отступа в скопированном блоке. Каскад возвращает абсолютные отступы, а не относительные маркеры.

Формат search/replace дополнительно пробует точное совпадение, совпадение с поправкой на пробелы и блоки с `...` вместо пропущенного кода. Мёртвый путь fuzzy matching из `aider/coders/editblock_coder.py:184` не копируется: поиск похожих строк с порогом не входит в контракт каскада.

Проверка каскада берёт 20 искажённых блоков со сдвинутым отступом, лишней пустой строкой, многоточием вместо кода и иным числом пробелов. Требование: не менее 18 успешных применений из 20. Baseline равен 0, потому что прикладного слоя правок в beacon нет.

Путь нового пакета, реализующего каскад, добавляется в поле «Владелец файлов» карточки до начала реализации. Реализация без этого поля не принимается.

<!-- executor-task:edit-cascade
editing-model: host-tools, unchanged
strategies: exact, three-way-git, line-diff-match-patch
variants: blank-lines, relative-indent
combinations: 12, first-success
indent: unique-marker-in, absolute-out
blocks: exact, whitespace-adjusted, ellipsis
no-fuzzy: editblock_coder.py:184
acceptance: 18-of-20
package-path: add-to-owner-files-before-implementation
executor-task:edit-cascade -->

## Единый конфиг путей артефактов

Исполнитель берёт пути артефактов из одного источника. Это `.beacon/config.yaml`, секция `paths`. Harbor делает так же в `.ai-factory/config.yaml`: там заданы `paths` и `language`. Путь, записанный в двух местах, проваливает карточку HD-052. Исполнитель не придумывает путь и не копирует его из другой карточки. Он читает значение из конфига.

Схема beacon-struct config для путей:

| Ключ | Дефолт | Назначение |
| --- | --- | --- |
| `paths.docs` | `docs` | документация и ADR |
| `paths.tasks` | `docs/tasks` | карточки задач |
| `paths.evals` | `evals` | eval-артефакты |

Конфиг содержит структуру `paths` с этими тремя полями. Неизвестный ключ или пустое значение даёт ошибку. Если файл отсутствует, действуют дефолты из таблицы. Языковые ключи `language.ui`, `language.artifacts` и `language.technical_terms` лежат в том же конфиге рядом с `paths` и описаны в HD-053.

<!-- executor-task:artifact-paths
source: .beacon/config.yaml
section: paths
keys: docs, tasks, evals
scattered-paths: fail
executor-task:artifact-paths -->

## Premise-check перед кодовым PR

Перед `gh pr create` исполнитель запускает `bash scripts/premise-check.sh <CARD> [--from-backlog <PATH>]`. Скрипт обновляет `origin/main` через `git fetch` и печатает один вердикт: `clean`, `landed` или `duplicate`. Вывод скрипта целиком идёт в тело PR. При `landed` или `duplicate` агент не открывает PR, а возвращает блокер с полным SHA и номером PR по правилу «Блокер с доказательством вместо PR-дубля».

Карточка обязана существовать в store как `docs/tasks/<CARD>.md` или быть явно взятой из бэклога. Для бэклога агент передаёт `--from-backlog <PATH>` и называет тот же путь в теле PR. Без одного из двух источников `clean` недоступен.

Скрипт не повторяет правила закрытия. Формат доказательства и whitelist шаблонов заголовков коммитов лежат в `docs/runbooks/CLOSE-PROCEDURE.md`, раздел «Whitelist шаблонов заголовков коммитов» (TCG-18). Проверка не меняет протокол Watch, текст `waiting, <watchID>` и число MCP-инструментов.

## Цикл: claim, register, push PR, finish turn

1. **claim.** Получить задачу через `beacon_task_claim`, если оркестратор ещё не выдал её. Сохранить ID, цель, зависимости и OwnerFiles из ответа. Не искать задачу вручную в `docs/tasks/`.
2. **register.** Вызвать `beacon_watch_pr` с `repo: "refusedguy/beacon"`, `pr`, `headSHA`, `role: "executor"` или `"fixer"` и `notify: { adapter: "opencode", target: "<ses_...>" }`. В `target` идёт РЕАЛЬНЫЙ идентификатор сессии из `_meta["ai.opencode/sessionID"]` или явно переданный `ses_...`. Значение `<cardID>` в `target` запрещено: wake будет доставлен не туда. Повторный вызов с теми же параметрами идемпотентен. Один агент может держать несколько Watch.
3. **push PR.** Перед `git worktree add ... origin/main` обязателен `git fetch origin main`: база берётся из свежего `origin/main`. Premise-check через `git merge-base --is-ancestor` делать только после fetch. Устаревшая база даёт ложный premise-check и конфликты. Для кодового PR дополнительно выполняется проверка из раздела «Premise-check перед кодовым PR». Сделать изменения в своём worktree и своей ветке от `origin/main`. Прямые коммиты в `main` запрещены. Локально запускать только `gofmt -w <изменённые_файлы>`. Перед пушем запускать синтаксический предпроход по изменённым файлам и не пушить при ошибке. Полные проверки прогоняет GitHub Actions CI на PR, не запускать `go test ./...`, `go build ./...` и `beacon gates` локально. Запушить ветку и открыть PR в `main` командой `gh pr create --base main --head <ветка>`. В отчёте указать номер PR, headSHA и список файлов.
4. **finish turn.** Завершить ход с отчётом ровно `waiting, <watchID>`, подставив сохранённый `watchID`. Не опрашивать состояние в цикле, не крутить `gh pr checks --watch`, не использовать `sleep` и не ждать вручную. Дальше агент спит до вердикта, который разрешает пробуждение.

## Локальный синтаксический предпроход

Перед пушем исполнитель проверяет синтаксис своих изменённых файлов. Предпроход парсит каждый файл деревом и собирает узлы с ошибкой. Для каждой ошибки он печатает `path:line`. Если найдена хотя бы одна ошибка, исполнитель чинит файл и повторяет предпроход. Исполнитель не пушит файл с ошибкой.

Предпроход не заменяет полные проверки. Он не запускает `go test ./...`, `go build ./...` и `beacon gates`. Полные проверки остаются в GitHub Actions. Локально по-прежнему разрешён только `gofmt -w` на свои `.go`-файлы.

Baseline карточки CA-14: пять опечаток вызвали пять лишних циклов CI. Предпроход останавливает все пять до пуша. Целевое число лишних циклов равно нулю.

<!-- executor-task:syntax-precheck
scope: changed-files
parse: syntax-tree
error-nodes: true
output: path:line
on-error: block-push
full-checks: ci-only
baseline-extra-ci-runs: 5
target-extra-ci-runs: 0
source: CA-14, docs/research/comp-aider.md
executor-task:syntax-precheck -->

## Документация в том же PR

Изменение кода требует обновления документации в том же PR. Исполнитель сверяет список файлов перед отправкой. Если PR меняет код в `internal/`, `cmd/` или другом исходном каталоге, затронутые docs обновляются в этой же ветке. Отдельный PR для документации допускается только по явной команде владельца.

Чек перед отправкой PR:

1. PR содержит изменение кода.
2. Для изменённого кода найдена затронутая документация.
3. Документация обновлена в этом же PR.

Расхождение между кодом и документацией проваливает карточку. PR с кодом без обновлённых docs возвращается исполнителю.

<!-- executor-task:docs-sync
code-change: update-docs-in-same-pr
mismatch: fail
executor-task:docs-sync -->

## Выбор забора для блоков кода

Документ или тело PR может включать блок, собранный из содержимого файла. Длину backtick-забора выбирают по этому содержимому. Забор из трёх backticks ломается, когда строка файла начинается с трёх или большего числа backticks: markdown закроет блок раньше времени.

Функция `ChooseFence` в `internal/app/prcreate/cards.go` перебирает длины от 3 до 12 и берёт первый забор, который не начинает ни одну строку содержимого. Перед проверкой снимаются ведущие пробелы и табуляции. Функция возвращает забор и флаг безопасности. Когда безопасного забора в диапазоне нет, вызывающий код печатает предупреждение и берёт самый короткий забор.

Правило относится к заново сгенерированному тексту: карточкам, телам PR и документам. Уже опубликованные PR не меняются.

## Обзор PR по ценности для ревьюера

Описание PR показывает изменения в порядке ценности для ревьюера. Исполнитель не печатает файлы в алфавитном порядке и не следует дереву каталогов. Он делит описание на три секции в фиксированном порядке: `core logic`, `wiring and integration`, `boilerplate and mechanical`. Core logic идёт первой. Boilerplate содержит только список имён без inline diff.

Когда change труден для чтения, исполнитель добавляет короткий псевдокод рядом с diff. Псевдокод передаёт намерение и порядок шагов, а не копию кода. Для простой правки псевдокод не нужен.

Исполнитель трассирует один конкретный вход. Маленький реалистичный пример проходит по старому и новому пути, и исполнитель называет место расхождения. Метки `Subtle`, `Breaking`, `Race condition` и `Perf` отделяют неожиданное. Метки необязательны. Текст объясняет, почему изменилось поведение, а не только что изменилось.

<!-- executor-task:review-value
order: core logic, wiring and integration, boilerplate and mechanical
no-tree-order: true
boilerplate: names-only, no-inline-diff
pseudocode: hard-to-read-change, optional-for-simple-edits
input-trace: one-small-realistic-input, name-divergence-point
labels: Subtle, Breaking, Race condition, Perf, optional
explain: why-not-only-what
source: CP-16, docs/research/comp-cursor-plugins.md
executor-task:review-value -->

## Адверсариальный разбор

Исполнитель проводит адверсариальный разбор, когда ревью требует доказательства поломки запуском. Несколько адверсариев пытаются сломать изменение. Каждый работает в отдельном worktree и получает одинаковый текст задачи и намерение изменения. Адверсарий не применяет правки к коду. Он возвращает команду и её вывод.

Каждая находка содержит пять полей.

| Поле | Что писать |
| --- | --- |
| намерение | что изменение должно делать и какое поведение сохранить |
| команда | запущенная команда, которую может повторить ревьюер |
| вывод | фактический вывод команды, не пересказ |
| требуемый вывод | ожидаемый вывод по намерению; расхождение и есть поломка |
| кто нашёл | адверсарий, который запустил команду |

Находка без команды и вывода не считается находкой. Замечания по стилю, именам и гипотетическим рискам без запуска в отчёт не попадают. Если подтверждённых finding нет, напиши отдельной строкой «нет подтверждённых finding». Это допустимый результат разбора.

Разбор не меняет `beacon_watch_pr`, число MCP-инструментов и протокол Watch.

<!-- executor-task:adversarial-review
fields: намерение, команда, вывод, требуемый вывод, кто нашёл
require: command-and-output-per-finding
reject: style, naming, hypothetical-without-run
no-findings: "нет подтверждённых finding"
adversaries: separate-worktrees, no-code-changes
protocol-change: none
source: PSK-02, docs/research/research-pstack-skills.md
executor-task:adversarial-review -->

## Контрольные точки правки и отмена

Исполнитель коммитит сразу после каждой правки и сразу после исправления линтера. Один коммит на правку даёт контрольную точку, к которой можно вернуться без работы с запушенной историей. Хеш каждого такого коммита попадает в `SessionCommits` якоря Watch рядом с другим состоянием worktree. Для записи хеша исполнитель вызывает `RecordCheckpoint`, для возврата `UndoCheckpoint` из `internal/infra/worktree/headguard.go`.

Отмена проверяет четыре условия aider: коммит сделан в этой сессии и лежит в `SessionCommits`, коммит не слияние, рабочее дерево чистое, ни одна ветка `origin/` не содержит коммит. Отмена запушенного коммита отклоняется. Команда также отклоняет коммит, который не является текущим HEAD. При успехе `UndoCheckpoint` возвращает файлы через `git checkout <parent> -- .`, затем двигает HEAD через `git reset --soft <parent>`.

После отмены HEAD стоит на предке SHA, записанного в Watch. `GuardWatch` продолжает защищать такой worktree по правилу `head_predecessor`, наряду с точным совпадением и потомком. Защита по точному head и предку сохраняется.

Контрольные точки остаются локальными коммитами в одной ветке. Число PR на задачу не меняется: один PR на карточку.

<!-- executor-task:checkpoints
commit-after: each-edit, each-linter-fix
store: WatchAnchor.SessionCommits, RecordCheckpoint
undo-guard: session-commit, not-merge, clean-tree, not-pushed
undo-restore: checkout-parent-dot, reset-soft-parent
watch-guard: head_exact, head_ancestor, head_predecessor
one-pr-per-card: unchanged
source: CA-04, docs/research/comp-aider.md
executor-task:checkpoints -->

## Обязательный текст отчёта

Отчёт о завершении хода содержит ровно строку `waiting, watchID`, где `waiting` литерал, а `watchID` подставляется из шага register. Любая другая формулировка не является корректным завершением хода.

<!-- executor-task:report
waiting, watchID
executor-task:report -->

## Предикат выхода и строка журнала

Предикат выхода входит в контракт Watch. Это проверяемое условие, которое агент записывает до первой итерации. Агент держит предикат неизменным на всех ревизиях ветки и во всех ребазах цепочки. Вердикт или плато не ослабляют предикат.

На каждой ревизии агент пишет одну строку журнала. В строке три поля: изменение, наблюдение, решение. Изменение называет правку или действие шага. Наблюдение называет факт, который агент увидел на этой ревизии. Решение называет следующий шаг и его связь с предикатом.

Следующий шаг агент берёт из поля `nextAction` ответа `beacon_get_result`, когда ответ есть. До ответа шаг следует из предиката. Другие источники шаг не задают. Плато, при котором условие не приблизилось к предикату, остаётся наблюдением. Плато не переводит Watch в готовность.

Wake-механизм и правило `waiting, <watchID>` не меняются. Вердикт агент берёт только из `beacon_get_result`.

<!-- executor-task:exit-predicate
predicate: verifiable-condition, set-before-first-iteration, unchanged-across-revisions
journal: one-line-per-revision, fields change-observation-decision
plateau: observation-only, no-ready, no-weakening
next-step-source: beacon_get_result.nextAction
protocol-change: none
source: CP-03, docs/research/comp-cursor-plugins.md
executor-task:exit-predicate -->

## Журнал решений decisions.tsv

Исполнитель ведёт один файл `decisions.tsv` на задачу. Одна строка файла описывает одно решение. В строке шесть колонок в порядке `ts`, `phase`, `decision`, `why`, `evidence`, `result`.

| Колонка | Содержание |
| --- | --- |
| ts | время решения в формате ISO 8601 |
| phase | фаза жизненного цикла из карты фаз |
| decision | принятое решение одним предложением |
| why | причина, по которой выбрано это решение |
| evidence | указатель на доказательство |
| result | исход, который агент наблюдал после решения |

Колонка `evidence` содержит указатель, а не описание. Указатель разрешается в SHA коммита, номер PR, путь `file:line` или артефакт. Читатель проверяет решение по указателю.

Исполнитель пишет только в конец файла. Он не переписывает и не удаляет старые строки. Если решение оказалось ошибкой, исполнитель добавляет новую строку. В колонке `decision` новой строки он пишет «перекрывает» и называет решение, которое она отменяет. Старая строка остаётся в файле.

Файл `decisions.tsv` по умолчанию рабочий артефакт. Исполнитель коммитит его только когда ревьюеру нужна история решений. Иначе ревьюеру достаточно указателя на строку в рабочем файле.

Раздел не меняет предикат выхода из раздела «Предикат выхода и строка журнала». CP-03 задаёт строку на ревизию Watch. Этот раздел задаёт файл и колонки для решений в течение задачи. Wake-механизм, `waiting, <watchID>` и число MCP-инструментов не меняются.

<!-- executor-task:decisions-journal
file: decisions.tsv
columns: ts, phase, decision, why, evidence, result
row: one-row-per-decision
evidence: SHA, PR, file:line, artifact, no-prose
write: append-only, no-history-edit
override: new-row-overrides-old, keep-old-row
commit: working-artifact-by-default, commit-when-reviewer-needs-history
relation: no-exit-predicate-change, no-protocol-change
source: PSKX-17, pstack/skills/show-me-your-work/SKILL.md:13-22
executor-task:decisions-journal -->

## Условие останова

Задача оптимизации метки получает условие останова до первой итерации. Условие берётся из карточки задачи. Оно задаёт пару полей: цель и минимум попыток. Цель формулирует движение метки выше шума. Минимум попыток это нижняя граница числа итераций.

Удачная ранняя попытка не заканчивает прогон. Исполнитель делает не меньше минимума попыток и только потом проверяет цель. Каждая попытка меняет одно условие и даёт один замер. Результат попытки попадает в журнал попыток из раздела «Журнал попыток» в `docs/perf/README.md`. Исполнитель читает журнал перед каждой следующей попыткой.

Журнал попыток отличается от файла `decisions.tsv` из раздела «Журнал решений decisions.tsv». Первый хранит девять колонок замера и служит оптимизации метки. Второй хранит шесть колонок решения и служит всей задаче. Исполнитель не смешивает эти файлы.

<!-- executor-task:stop-condition
fields: goal, min-attempts
lower-bound: min-attempts, independent-of-first-success
early-success: does-not-stop-run
per-attempt: one-change, one-measurement
attempt-journal: docs/perf/README.md, nine-columns, read-before-each-attempt
decisions-journal: separate, six-columns, unchanged
executor-task:stop-condition -->

## Причина решения

Когда исполнитель отвечает на вопрос «почему так было решено», он заполняет три поля.

| Поле | Содержание |
| --- | --- |
| Ссылка на record | Указатель на коммит, PR или ADR, где автор зафиксировал решение. |
| Вывод агента | Собственное умозаключение с пометкой «вывод агента». Не подменяет слова автора. |
| Источник не отвечает | Строка «источник не отвечает», если в record нет ответа. |

Исполнитель не выдаёт свой вывод за цитату. Если источник не отвечает, исполнитель пишет «источник не отвечает» и не добавляет версию от себя. Правило относится к ответам, телам PR и документам. Оно не меняет протокол Watch и число MCP-инструментов. Источник: `pstack/skills/why/SKILL.md:9`, карточка PSKX-20.

<!-- executor-task:decision-reason
fields: source-reference, agent-inference, source-silent
inference-label: вывод агента
silent-label: источник не отвечает
source-ref: commit, PR, ADR
scope: answers, pr-body, docs
protocol-change: none
source: pstack/skills/why/SKILL.md:9, PSKX-20
executor-task:decision-reason -->

## Вердикты и пробуждение

Авто-мерж включён по умолчанию: Beacon сам мержит зелёный PR по `verdict=ready` (ADR 0072). Исполнитель не получает команды мерж и не вызывает `gh pr merge`.

При терминальном вердикте `merged` Beacon переводит связанную карточку в `done` и пишет ссылку на PR в подтверждение. Исполнитель карточку не закрывает и статус вручную не меняет.

Исполнитель просыпается только когда мерж невозможен: на `keep_working`, `rebase`, `retry` или при ошибке мержа. Ошибка мержа приходит как один из этих вердиктов и не требует нового Watch. Переход `watching` в `watching` не двигает revision и никого не будит.

| вердикт | пробуждение |
| --- | --- |
| keep_working | да |
| rebase | да |
| retry | да |
| ready | нет |
| watching | нет |
| merged | нет |
| closed | нет |
| timeout | нет |

<!-- executor-task:verdicts
wake: keep_working, rebase, retry
nowake: ready, watching, merged, closed, timeout
executor-task:verdicts -->

## Исправление неверного действия до терминального отказа

Три ошибки действия агент исправляет сам, без слота оркестратора: неверный формат действия, запрещённое действие и синтаксис команды. Для каждой ошибки агент получает отдельный шаблон ответа. Шаблон называет причину и просит прислать одно исправленное действие.

| Ошибка | Код | Шаблон ответа |
| --- | --- | --- |
| неверный формат действия | `action_format` | `ActionRequeryTemplate(ActionFormatError)` |
| запрещённое действие | `blocked_action` | `ActionRequeryTemplate(ActionBlockedAction)` |
| синтаксис команды | `command_syntax` | `ActionRequeryTemplate(ActionCommandSyntax)` |

Бюджет равен трём повторам: константа `MaxActionRequeries` в `internal/infra/mcp/tool_wait_result.go`. Счётчик общий для трёх ошибок. Четвёртая ошибка подряд заканчивает прогон с терминальным исходом `exit_format`. Исправленная попытка попадает в траекторию через `ActionTrajectory`, но не попадает в видимую историю сообщений и не меняет `WaitResultOutput`, вердикт или `WaitStopReason`.

Ошибка API, среды или рантайма не исправляется повтором. Такой исход заканчивает прогон сразу со своим кодом, например `exit_api` или `exit_environment_error`, и не расходует бюджет повторов.

Бюджет повторов действия не равен бюджету повторов красных проверок `FlakyConfig.MaxRetries` и не добавляет значений в `WaitStopReason`. Правило пробуждения не меняется: агент просыпается только на `keep_working`, `rebase` и `retry`. Ошибка формата действия, которую агент исправил сам, не расходует слот оркестратора.

<!-- executor-task:action-requery
errors: action_format, blocked_action, command_syntax
templates: per-error, ActionRequeryTemplate
budget: MaxActionRequeries, 3, shared-counter
terminal: exit_format
trajectory: ActionTrajectory, not-visible-history
separate-from: FlakyConfig.MaxRetries, WaitStopReason
wake: unchanged, keep_working-rebase-retry
source: SB-16, docs/research/comp-swebench.md, sweagent/agent/agents.py
executor-task:action-requery -->

## Действие после retry

Вердикт `retry` требует классификации до конца хода. Исполнитель читает `failedChecks` через `beacon_get_result` и сверяет их со своим diff. Отвечать `waiting, <watchID>` сразу после retry запрещено. Такой ответ оставляет задачу без владельца и повторяет инцидент 334, где агент дважды ответил waiting и не написал blocked.

После классификации исполнитель делает одно из двух. Если упавшая проверка лежит в его diff, он исправляет ветку и пушит новый headSHA. Если причина вне его diff, он вызывает `beacon_ack` с `decision="blocked:not_mine"` и непустой причиной. Действие выполнено до фразы ожидания, и только после неё исполнитель завершает ход.

## Повторная ошибка: уровень исправления

Класс ошибки считается повторным после двух независимых случаев в разных сессиях или карточках. Один случай остаётся наблюдением. Источник: `pstack/skills/correct/SKILL.md:15-30`.

Исполнитель не чинит повторную ошибку вслепую. Он сначала выбирает уровень исправления по порядку: архитектура, типы, линт, тест, документация. Документацию берёт последней. Таблица «Правило, класс ошибки, чем обеспечено» в `AGENTS.md` показывает текущий контроль для класса.

Выбранный контроль обязан падать на реальный прошлый случай. Исполнитель запускает его на этом случае до правки и показывает вывод. Контроль, который не падает на прошлом случае, исправлением не считается. Если в строке таблицы стоит «долг» или поле «Чем обеспечено» пусто, класс остаётся неисполненным и повтор не принимается как исправленный.

<!-- executor-task:fix-level
trigger: repeated-error, two-independent-cases
levels: architecture, types, lint, test, docs-last
table: AGENTS.md "Таблица правил"
falsifier: new-control-fails-on-real-past-case
debt: not-fixed
source: PSKX-04, pstack/skills/correct/SKILL.md:15-30
executor-task:fix-level -->

## Пропуск недоступного шага

Адаптер поддерживает не все шаги контракта. Если инструмента или шага нет, исполнитель не останавливается. Он пишет одну строку пропуска и переходит к следующему шагу.

Формат строки: `Skipped in <adapter>: <step>. <reason>.`. Для адаптера opencode строка выглядит как `Skipped in opencode: <step>. <reason>.`. Таблица соответствий адаптеров и пример строки лежат в `README.md` в разделе «Таблица соответствий адаптеров».

Исполнитель не задаёт вопрос владельцу из-за отсутствующего шага и не повторяет попытку. Причина пропуска идёт в той же строке. Блокер с доказательством из раздела о premise остаётся отдельным случаем. Там причина не в отсутствии инструмента, а в уже закрытой задаче.

<!-- executor-task:skip-step
format: Skipped in <adapter>: <step>. <reason>.
stop: forbidden
ask-owner: forbidden
retry: forbidden
executor-task:skip-step -->

## Внешние поломки main

Если проверка красная и проблема на `main`, Beacon сам поставит Watch `blocked` и запишет `BlockerIncident`. Исполнитель не чинит чужую поломку.

Если исполнитель увидел внешнюю поломку локально на `origin/main`, он вызывает `beacon_ack` с `decision="blocked:not_mine"` и непустой причиной, затем завершает ход. Оркестратор просыпается по этому сигналу и запускает FIX-агента.

## Информационные проверки не являются блокировкой main

Проверка с `required: false` в `beacon_list_running` или с `continue-on-error: true` в workflow является информационной. Такой проверкой является гейт `docs` из `.github/workflows/docs.yml`: он сообщает о дублях ADR и статусах карточек, но не блокирует merge.

Информационная проверка НЕ является `blocked.main` и НЕ является `failedCheck`, требующим починки. По её `FAILURE` НЕ вызывать `beacon_ack` с `decision="blocked:not_mine"`. Блокер main объявляется только по required-проверке с реальной ошибкой в коде main, которая ломает сборку или тесты. Информационный `docs`-гейт с дублями ADR к таким не относится.

Основание: в PR #434 (HD-045) и #437 (BS.10) исполнители вызвали `beacon_ack decision=blocked:not_mine` по `FAILURE` информационного `docs`-гейта и заблокировали свои PR. Это ложная блокировка: все проверки в ответе `beacon_list_running` имели `required: false`, а сам workflow помечен `continue-on-error: true`.

<!-- executor-task:informational-checks
required:false -> informational, no blocked:not_mine
continue-on-error:true -> informational, no blocked:not_mine
docs gate with duplicate ADRs -> informational, not a main blocker
executor-task:informational-checks -->

## Триаж красного вердикта

На вердикте `retry` исполнитель не чинит код вслепую. Он читает вывод упавшего теста из `failureExcerpt`, сравнивает `failedChecks` со своим diff и только потом решает, что делать (ADR 0072, ADR 0155). Классификация уходит wake'ом оркестратору, а не вопросом владельцу.

<!-- executor-task:triage
source: beacon_get_result.failedChecks
fields: name, conclusion, detailsUrl, required, flakyMatched
excerpt: beacon_get_result.failureExcerpt
excerpt-flags: failureExcerptTruncated, failureExcerptLogUrl, failureExcerptUnavailable
compare: git diff <baseRef>..HEAD
own-diff: fix-branch-and-push-new-headSHA
foreign-or-flaky: blocked:not_mine
unknown: report-no-own-changes
fix-main: forbidden
wake: orchestrator
executor-task:triage -->

Процедура:

1. Прочитать снимок `beacon_get_result`. В ответе есть `failedChecks` с полями `name`, `conclusion`, `detailsUrl`, `required` и `flakyMatched` и необязательный вывод упавшего теста `failureExcerpt`. Полный лог нужен только при `failureExcerptTruncated: true`.
2. Сверить `failedChecks` со своим diff: `git status --short` и `git diff <baseRef>..HEAD` в своём worktree. Проверка считается твоей, когда её причина лежит в файлах, которые ты менял в этой ветке.
3. Выбрать причину и отослать wake подписчику `role=orchestrator` на том же Watch.
   - Причина вне твоего diff или красный `main` на последнем коммите: флейк или чужая поломка, `decision="blocked:not_mine"`.
   - Причина в твоём diff: твоя ошибка. Исправить ветку и запушить новый headSHA.
   - Причину нельзя отнести ни к тому, ни к другому: сообщить, что изменений моих нет.
4. Завершить ход. Wake оркестратору не означает готовность PR. Мерж делает только оркестратор после своего `beacon_get_result` с `verdict=ready`.

Не чинить `main` самому, не пушить в `main` и не править файлы вне своего worktree.

## Вывод упавшего теста в wake

Wake по-прежнему несёт вердикт и указатель на проверку. Вывод упавшего теста приходит в том же снимке `beacon_get_result`, рядом с `failedChecks`. Исполнитель читает его до классификации причины.

| Поле | Где | Назначение |
| --- | --- | --- |
| `failedChecks[].failureExcerpt` | запись проверки | короткий очищенный вывод этой проверки |
| `failedChecks[].failureExcerptUnavailable` | запись проверки | источник вывода не ответил для этой проверки |
| `failureExcerpt` | ответ целиком | суммарный вывод упавших проверок, не более 2 КБ |
| `failureExcerptTruncated` | ответ целиком | вывод обрезан по лимиту |
| `failureExcerptLogUrl` | ответ целиком | ссылка на полный лог проверки |

Поля необязательны. Без источника вывода ответ сохраняет прежнюю форму. Отсутствие вывода не означает, что проверка прошла.

Порядок чтения:

1. Взять `failureExcerpt` из ответа или из записи `failedChecks` нужной проверки. Это и есть вывод упавшего теста. Он уже ограничен 2 КБ и очищен на границе MCP.
2. Если `failureExcerptTruncated` равен `true` или фрагмент заканчивается на `...[truncated]`, открыть `failureExcerptLogUrl` и дочитать полный лог. Без этого флага полный лог не нужен.
3. Если `failureExcerptUnavailable` равен `true`, источник вывода недоступен. Причину определять по `name`, `conclusion` и `detailsUrl`, а блокирующий характер проверки по `required`. Отсутствие вывода само по себе не повод для `blocked:not_mine`.
4. Сверить вывод со своим diff по правилу триажа. Вывод указывает на файл и строку, но не заменяет `git diff <baseRef>..HEAD`.
5. Классификацию и вердикты не менять. `required: false` и `continue-on-error: true` остаются информационными проверками. Ограничение вывода задаёт Beacon, исполнитель не вставляет полные логи в wake и отчёт.

Основание: карточка CA-05 и `docs/research/comp-aider.md`. Порог проверки: не менее 7 починок с первой попытки из 10 при baseline, измеренном отдельно.

<!-- executor-task:test-output
source: beacon_get_result.failureExcerpt
per-check: failedChecks[].failureExcerpt
aggregate-limit: 2KB
truncated-marker: ...[truncated]
full-log: failureExcerptLogUrl
unavailable: failureExcerptUnavailable
classification: unchanged
verdicts: unchanged
executor-task:test-output -->

## Подтверждение вставки вывода команды

Вывод команды не уходит в контекст исполнителя по размеру молча. `PrepareWakeOutput` в `internal/infra/github/rest.go` измеряет вывод до вставки: байты, строки и оценку токенов. Оценка совпадает с учётом контекста: одно слово даёт один токен, каждые четыре байта слова дают ещё один. Один и тот же текст всегда даёт одну цену и один вопрос.

| Решение | Условие | Что вставляется |
| --- | --- | --- |
| автоматическая вставка | упавшая проверка | фрагмент не более 2 КБ |
| автоматическая вставка | малый вывод, не более 1024 токенов | весь вывод |
| запрос подтверждения | вывод дороже 1024 токенов, ответа нет | ничего |
| согласие | вывод дороже 1024 токенов, ответ есть | весь вывод |

Вопрос звучит как `Add Nk tokens of command output to the chat?`, где `N` берётся из `OutputStats.Tokens`. Отказ оставляет `WakeOutput.Text` пустым: в контекст не попадает ни один байт. Отчёт показывает число строк и токенов из `OutputStats` даже при отказе, поэтому цена вставки видна до решения.

Вердикты и классификация проверок не меняются. Порог управляет только вставкой вывода. Упавшие проверки сохраняют автоматический фрагмент с прежним лимитом 2 КБ и прежними полями `failureExcerpt*`.

Baseline карточки CA-24: цена вставки не измерялась, все десять выводов попадали в контекст без оценки. Замер счётчиком `MeasureOutput`: малый вывод из 83 байт и двух строк стоит 25 токенов, вывод из 102200 байт (около 100 КБ) и 1400 строк стоит 32200 токенов. Порог 1024 разделяет их и сохраняет автоматическую вставку малых выводов.

Вызывающая сторона в `internal/infra/mcp/tool_get_result.go` должна применять `PrepareWakeOutput` перед заполнением `FailureExcerpt`. Эта часть лежит вне OwnerFiles карточки CA-24 и переносится отдельной проводкой.

<!-- executor-task:output-confirmation
source: internal/infra/github/rest.go
measure: OutputStats, MeasureOutput
prompt: Add Nk tokens of command output to the chat?
threshold-tokens: 1024
failed-check: automatic, bounded-at-2KB
on-refusal: text-empty
report: lines-and-tokens
verdicts: unchanged
baseline: 100KB-output-32200-tokens
executor-task:output-confirmation -->

## От красной команды к минимальному репро

Красная команда из раздела «Диагностика» в `AGENTS.md` даёт сигнал, но не называет причину. Исполнитель не правит код сразу. Он сжимает репро и ранжирует гипотезы по правилам `docs/research/comp-matt-skills.md` (источник: `mattpocock/skills`, `skills/engineering/diagnosing-bugs/SKILL.md:78-112` и `:130-138`).

<!-- executor-task:repro-hypotheses
source: AGENTS.md diagnostics red command
steps: shrink-repro, rank-hypotheses, show-before-instrumentation, one-variable
hypotheses: 3-5, falsifiable, each with a predicted effect
log-tag: [DEBUG-<short-id>], remove-before-finish
perf: baseline-before-change
depth: defer to internal/domain/depth.go, no duplicate checks
executor-task:repro-hypotheses -->

Исполнитель выполняет четыре шага по порядку, до любой инструментации.

1. Сжать репро. Удалять по одному элементу и после каждого удаления перезапускать красную команду. Репро готов, когда удаление любого оставшегося элемента делает цикл зелёным. Оставшиеся элементы называют load-bearing. Элемент, при удалении которого команда остаётся красной, нагрузки не несёт и уходит из репро.
2. Ранжировать гипотезы. Составить от трёх до пяти опровержимых гипотез и отсортировать их по правдоподобию. Для каждой записать предсказание вида «если причина X, то изменение Y уберёт баг». Гипотеза без предсказания не годится. В `internal/domain/depth.go` уже есть проверка `hypothesis` и `falsifier` с одной гипотезой на переход. Её не дублируют.
3. Показать список до инструментации. Отправить ранжированный список оркестратору wake'ом на том же Watch или владельцу. Делать это до правки кода и до установки логов. Показ списка не означает готовность PR. Мерж остаётся за оркестратором.
4. Менять одну переменную. Каждый эксперимент меняет ровно одно условие. Результат сверяют с предсказанием из шага 2. Гипотеза, чьё предсказание не сбылось, отбрасывается. Переход на новый слой требует новых данных по `internal/domain/depth.go:93-115`. Данные из старого репро не переносят.

Временные логи помечать уникальным тегом `[DEBUG-<короткий-id>]`. Тег ставится в каждую строку, добавленную для диагностики. Перед завершением хода исполнитель удаляет все строки с этим тегом. Тег, оставшийся в PR, проваливает карточку.

Для регрессий производительности исполнитель сначала замеряет baseline и записывает число в карточку по правилу «Измерение до выбора» в `AGENTS.md`. Правка без замера не принимается.

Не менять `internal/domain/depth.go` и не повторять его проверки. Шаблон добавляет только сжатие репро, ранжирование и внешний порог показа гипотез.

## Блокер с доказательством вместо PR-дубля

Исполнитель проверяет premise карточки до работы. Если требование уже закрыто другим изменением, он не открывает второй PR. Причины блокера: изменение уже влито в `origin/main`, готов чужой PR с тем же изменением, владелец принял решение, premise карточки ложна. В каждом случае исполнитель возвращает блокер оркестратору с доказательством, а не создаёт дубль.

Доказательство должно быть проверяемым. Исполнитель кладёт в причину следующее.

- Уже влито: полный SHA коммита, номер или ссылка на PR и результат `git merge-base --is-ancestor <SHA> origin/main` с кодом выхода 0. Команда с кодом 0 означает, что коммит уже в main.
- Дубль или чужой PR: ID другой карточки или номер существующего PR и SHA коммита, который уже несёт изменение.
- Решение владельца: ссылка на сообщение владельца с самим решением, а не пересказ.
- Ложная premise: путь и символ в `internal/`, где функциональность уже есть, и ссылка на ADR, если решение зафиксировано.

Исполнитель шлёт блокер через `beacon_ack` с `decision="blocked:not_mine"` и непустой причиной, содержащей доказательство. Если obligation ещё нет, он отдаёт тот же блокер с доказательством в отчёте хода оркестратору. Затем он завершает ход по общему правилу `waiting, <watchID>`. Второй PR, повторный коммит и ручное закрытие карточки запрещены: карточку закроет оркестратор по `docs/runbooks/CLOSE-PROCEDURE.md`.

<!-- executor-task:blocker-evidence
premise-check: before-work
already-merged: git merge-base --is-ancestor <SHA> origin/main, exit 0, SHA, PR
duplicate-or-foreign-pr: other-card-or-PR, SHA
owner-decision: link-to-owner-message
false-premise: path-and-symbol-in-internal, adr-ref
duplicate-pr: forbidden
manual-close: forbidden
close-runbook: docs/runbooks/CLOSE-PROCEDURE.md
executor-task:blocker-evidence -->

## Отложенная проводка (deferred wiring)

Если часть фичи осталась не подключённой в этом PR, исполнитель не оставляет её молча «на потом». Он вызывает `beacon_defer_wiring` с `watchID`, номером PR, причиной и списком `OwnerFiles`, затем добавляет в свой PR сгенерированную карточку `docs/tasks/WIRING-<PR>.md` по шаблону `docs/templates/wiring-card.md`.

Тулза пишет маркер `.beacon/wiring-pending` с одной строкой `WIRING:<PR>` на каждую отложенную проводку. Локальный гейт `WIRING-*` читает маркер и карточки из дерева: маркер без карточки, карточка без `Статус: todo`, пустой `OwnerFiles` или отсутствующая `## Причина` проваливают проверку. Маркер и карточка должны попасть в один PR.

## Диагностика вместо гаданий и sleep

Не грепать логи вручную и не ждать в bash. Использовать `beacon_list_running` для снимка активных Watch, `beacon_stats` для счётчиков, `beacon inspect` для разбора конкретного Watch и `beacon logs --tail 50 --errors` для последних ошибок.

## Re-run проверок

Повтор проверок делает агент своими правами командой `gh run rerun --failed`. Beacon не выполняет эту команду от имени агента и не запускает проверки. При сбросе suite Beacon меняет вердикт на `watching` и увеличивает revision, после чего агент сам решает, повторить проверку или переделать код.

<!-- executor-task:rerun-owner
agent
executor-task:rerun-owner -->

## Нет TTL в цикле

У Watch нет жесткого TTL по времени. Watch закрывается по терминальному вердикту, по отмене или по смене headSHA (ADR 0019). Один агент может делать множество rebase в одной цепочке, число rebase не ограничено. Долгая цепочка rebase не обрывается по времени. Orphan с неактивным подписчиком обрабатывается эскалацией оркестратору, а не тихим удалением Watch.

<!-- executor-task:ttl
none
executor-task:ttl -->

## Структурный ответ при неудачной правке

Блок search/replace, который не совпал с файлом, возвращает причину и похожие строки. Общий отказ без причины больше не считается ответом. Основание: `docs/research/comp-aider.md`, дополнение к CA-03, `aider/coders/editblock_coder.py:602-630` и `:82-124`.

Ответ несёт одно состояние на блок и общий список блоков для повторной отправки.

| Поле | Значение | Назначение |
| --- | --- | --- |
| `applied` | `true` | блок применён, повторно не отправляется |
| `category` | `failed-block`, `already-applied` | причина, по которой блок не применён |
| `similar` | строки целевого файла | похожие строки, порог 0.6 |
| `resend` | индексы блоков | только неудачные блоки, требующие исправления |

Порядок обработки блока:

1. Сравнить SEARCH с файлом после сжатия пробелов. Совпадение помечает блок `applied: true`. Такой блок не попадает в `resend`. Повторная отправка применённого блока нарушает контракт.
2. Проверить «REPLACE уже есть в файле». Если замена уже присутствует после сжатия пробелов, вернуть `category: already-applied`. Повторная отправка такого блока тоже не нужна.
3. Для остальных блоков вернуть `category: failed-block` и похожие строки. Строка файла считается похожей, когда оценка longest common subsequence по нормализованному тексту не ниже 0.6. Нормализация сжимает пробелы, поэтому сдвиг отступа и лишние пробелы не скрывают совпадение.
4. Похожие строки остаются советом. Инструмент не применяет нечёткое совпадение молча. Каскад из CA-03 решает успех только точным совпадением.
5. Приёмка берёт 20 искажённых блоков со сдвинутым отступом, лишней пустой строкой, многоточием и иным числом пробелов. Требование: не менее 18 ответов содержат похожую строку из файла, а все успешные блоки помечены как применённые. Baseline равен 0 из 20.

Поля ответа и порог реализованы в `internal/infra/batchapply/apply.go` функциями `evaluateEditBlocks`, `similarEditLines` и `replaceAlreadyInFile`.

<!-- executor-task:edit-failure
threshold: 0.6
similarity: whitespace-collapsed-lcs
categories: failed-block, already-applied
similar-lines: advice-only, no-auto-apply
resend: failed-blocks-only
already-applied: replace-in-file
acceptance: 18-of-20
executor-task:edit-failure -->

## Аудит подавлений

Проход «аудит подавлений» проверяет комментарии и подавления правил линтера. Цель прохода: доказать каждое исключение или пометить его на удаление. Агент не меняет код в этом проходе. Ему известны только комментарии и отчёт. Результат прохода только отчёт. Агент не записывает файлы.

Агент допускает пять исключений.

1. Юридический заголовок файла.
2. Поведение, навязанное внешней зависимостью или протоколом.
3. Директива `prettier-ignore`.
4. Doc comment публичного контракта.
5. Ссылка на issue или RFC.

Любое другое оправдание исключением не считается. Длинное оправдание без доказанного исключения агент помечает на удаление.

Агент проверяет каждое подавление `eslint-disable`, `@ts-ignore` и `@ts-expect-error` по правилу линтера, которое подавление отключает. Для каждого подавления отчёт содержит имя правила, причину и вердикт. Если подавление прячет реальную ошибку, агент помечает символ `MUST KILL`. Пометка означает, что символ нужно исправить в обычном цикле с OwnerFiles, а не в этом проходе.

Отчёт содержит таблицу с четырьмя полями.

| Поле | Значение | Назначение |
| --- | --- | --- |
| `place` | файл и строка | где найден комментарий или подавление |
| `finding` | текст комментария или подавление | что проверял агент |
| `rule` | имя правила линтера | какое правило отключено |
| `verdict` | `доказано`, `удалить`, `MUST KILL` | что делать с находкой |

<!-- executor-task:suppression-audit
exceptions: 5
suppressions: eslint-disable, @ts-ignore, @ts-expect-error
verdicts: proven, remove, MUST KILL
write: forbidden, report-only
scope: comments-and-suppressions
source: pstack/agents/comment-sicko.md, CP-17
executor-task:suppression-audit -->

## Предложение по кодификации

Когда исполнитель встречает ограничительный комментарий, он предлагает проверку вместо удаления. Проверкой может быть тип, runtime-проверка, тест или CI-линт. Исполнитель берёт самый дешёвый контроль, который падает на реальный прошлый случай.

Отчёт хода содержит поле `предложение по кодификации`. Поле несёт путь и строку комментария, выбранный контроль и ссылку на случай, где контроль падает. Если контроль найти не удалось, поле сообщает, что комментарий остаётся открытым ограничением. Исполнитель не удаляет комментарий без одобрения владельца. После одобрения он вносит контроль и удаляет комментарий, оставляя ссылку на контроль.

Фраза завершения хода `waiting, watchID` не меняется.

<!-- executor-task:codification-proposal
trigger: limiting-comment
field: codification-proposal
control: type, runtime-check, test, ci-lint
select: cheapest-control-that-fails-past-case
approval: before-deletion
report-phrase: unchanged
source: pstack/skills/no-comments/SKILL.md:23, PSKX-13
executor-task:codification-proposal -->

## Сверка объявленных компонентов перед PR

Перед открытием PR исполнитель сверяет объявленные пути компонентов с реальными файлами. Для каждого затронутого плагина он читает манифест и проверяет, что каждый заявленный путь указывает на существующий файл внутри каталога плагина. Все проверки манифеста описаны в `AGENTS.md` в разделе «Проверки манифеста».

Исполнитель проходит по шести проверкам.

| Проверка | Что сверяется |
| --- | --- |
| 1 | манифест существует и содержит имя |
| 2 | пути относительные и без `..` |
| 3 | объявленные пути компонентов совпадают с реальными файлами |
| 4 | у правил, скиллов, агентов и команд есть frontmatter с обязательными полями |
| 5 | область плагина узкая, установка описана в README |
| 6 | новые плагины сохраняются в каталог по умолчанию, если пользователь не попросил иначе |

Расхождение между манифестом и файлом блокирует PR: исполнитель правит манифест или добавляет файл до открытия PR. Проверка путей: относительные пути с `..` и пути за пределами каталога плагина не принимаются. Для каждого компонента с обязательным frontmatter исполнитель проверяет наличие полей до сдачи. Проверка не меняет индекс карточек, который по-прежнему собирается командой `beacon index build`. Хук `execute.before` и число MCP-инструментов остаются без изменений.

<!-- executor-task:manifest-checks
when: before-pr
six-checks: manifest-exists-and-name, relative-paths-no-dotdot, declared-paths-match-files, frontmatter-required-fields, narrow-scope-readme, default-catalog
block-on-mismatch: true
index-unchanged: beacon-index-build
protocol-change: none
source: CP-18, docs/research/comp-cursor-plugins.md
executor-task:manifest-checks -->

## Запреты

- Не запускать опрос в цикле вместо завершения хода.
- Не выдавать готовность по idle-отчёту и по вердикту `ready` без решения Beacon или оркестратора.
- Не поручать Beacon команды, которые выполняет агент. Beacon только детектирует состояние и шлёт указатель на запись.
- Не читать внутренние логи и базы агента как источник результата (ADR 0027).
- Не закрывать чужие карточки в несвязанных PR.
- Не писать в файл с ролью `readonly`. Запись отклоняется с кодом `readonly_file`.
- Не запускать `batch-apply` без явной команды владельца.
- Не отправлять PR с изменением кода без обновления документации в том же PR.
- Не искать задачу вручную в `docs/tasks/` вместо `beacon_task_claim`.
- Не добавлять зависимость, которая замыкает цикл в графе.
- Не переводить карточку в `done` вручную: это делает Beacon при `merged`.
- Не открывать PR-дубль, когда требование карточки уже закрыто другим изменением.
- Не возвращать блокер без доказательства: SHA, код `merge-base`, PR или ссылка на решение владельца.
- Не вставлять полный CI-лог в wake или отчёт вместо ограниченного `failureExcerpt`.
- Не пушить изменённый файл с синтаксической ошибкой, найденной предпроходом.

## Самопроверка шаблона

Тест шаблона лежит в `tests/adapter/executor-template.test.js` и читает этот файл через `node:fs`. Он разбирает маркеры и проверяет четыре условия карточки: `ready` и `watching` не будят, `keep_working`, `rebase` и `retry` будят, текст `waiting, watchID` присутствует, у Beacon нет команды от имени агента. Дополнительно проверяется, что re-run принадлежит агенту и TTL отсутствует.

Блок `executor-task:claim` фиксирует, что задача приходит через `beacon_task_claim`, цикл отвергается, при `PERSIST_FAILED` оркестратор получает `blocked:not_mine`, а карточку в `done` переводит Beacon при мерже.

Блок `executor-task:repro-hypotheses` фиксирует четыре шага после красной команды, тег `[DEBUG-...]` с обязательным удалением и замер baseline для perf-регрессий. Проверка карточки CM-02 ищет в шаблоне `load-bearing`, четыре шага и тег отладочных логов. Контракт гипотезы в `internal/domain/depth.go` шаблон не дублирует.

Блок `executor-task:token-accounting` фиксирует пять категорий токенов, методы учёта и порог предупреждения 1024. Карточка CA-07 проверяет, что сумма по категориям совпадает с эталоном, остаток вычислен по `max_input_tokens` и предупреждение срабатывает на пороге.

Блок `executor-task:subtract-first` фиксирует подшаг «Вычитание» как первый в фазе plan, proof для каждого удаления и запрет спекулятивных проверок. Карточка PSKX-31 ищет в шаблоне `подшаг Вычитание` и запрет guard'ов без требования.

Блок `executor-task:test-output` фиксирует поля вывода упавшего теста, лимит 2 КБ, флаг обрезки, ссылку на полный лог и сохранение классификации и вердиктов.

Блок `executor-task:output-confirmation` фиксирует измерение вывода, порог подтверждения 1024 токенов, пустой текст при отказе и автоматический фрагмент для упавших проверок. Карточка CA-24 проверяет, что большие выводы не попадают в контекст без согласия.

Блок `executor-task:syntax-precheck` фиксирует разбор изменённых файлов деревом, вывод `path:line` и блокировку пуша при узле с ошибкой. Карточка CA-14 задаёт этот блок. Полные проверки остаются в CI.

CI подхватывает тест шагом `Test JS adapters` в `.github/workflows/ci.yml`: `find tests/adapter -maxdepth 1 -name '*.test.js' | xargs node --test`. Тест использует только встроенные модули Node.

## Объяснение подсистемы одним read-only агентом

На вопрос «как это работает» исполнитель запускает одного агента с правом только на чтение. Это read-only объяснение, а не исправление; объяснение подсистемы за один проход даёт архитектурную картину инженеру, который не знаком с областью. Цель агента — покрыть поток данных и владельцев состояния, а не перечислить функции и файлы.

Агент получает вопрос и грань подсистемы. Он читает код, карточки и ADR, не меняя файлы, и возвращает объяснение без существенной переделки. Один проход означает один ответ: уточняющие итерации и вторая агентская сессия в этом режиме не нужны. Если ответ требует правки кода, агент сообщает об этом и возвращается к обычному циклу с карточкой; смешивать объяснение с исправлением запрещено.

Режим объяснения не создаёт коммит, не меняет файлы и не открывает PR. Он не регистрирует Watch, не вызывает `beacon_task_claim` и не переводит карточку в `done`. Роль `readonly` из блока `executor-task:context-roles` распространяется на все файлы, которые агент видит в этом режиме: запись в них отклоняется. Обычный цикл с OwnerFiles и PR продолжает действовать для задач на изменение.

Источник приёма: `pstack/skills/how/SKILL.md:15-17` (`readonly: true`). Карточка PSKX-08 фиксирует приём в дереве beacon, не создавая новый глобальный скилл и не меняя протокол Watch. Приём не дублирует кандидатов PSK-01, адверсарии PSK-02, TLA Watch PSK-03, CP-01..05 и CM-01..05.

<!-- executor-task:read-only-explain
trigger: how-question, one-agent
agent: single, readonly
passes: one
audience: engineer-unfamiliar-with-area
cover: data-flow, state-owners
not: function-list
write: forbidden
commit: none
pr: none
watch: none
mixing-with-fix: forbidden
role: readonly-from-context-roles
source: pstack/skills/how/SKILL.md:15-17, PSKX-08
scope: no-new-skill, no-protocol-change
executor-task:read-only-explain -->

## Политика действий до выполнения (SB-15)

Исполнитель проверяет каждое действие по политике из `AGENTS.md`, раздел «Политика действий агента», до запуска. Политика задана тремя формами: `blocklist_prefix`, `blocklist_standalone` и `block_unless_regex`. Проверка останавливается на первом совпадении и возвращает `refuse` с именем правила. Исполнитель меняет действие и проверяет его снова. Запрещённое действие не доходит до выполнения.

| Действие | Форма правила | Вердикт |
| --- | --- | --- |
| `nohup cmd` | `blocklist_prefix`, префикс `nohup` | refuse |
| отдельное `python` | `blocklist_standalone`, точное совпадение `python` | refuse |
| `radare2 file` | `block_unless_regex`, флаг `-c` отсутствует | refuse |
| `radare2 -c cmd file` | `block_unless_regex`, флаг `-c` есть | разрешено |

Политика не дублирует правила хука `execute.before` и не добавляет инструментов MCP. Проверка оболочки работает как раньше.

<!-- executor-task:action-policy
trigger: before-action
forms: blocklist-prefix, blocklist-standalone, block-unless-regex
match: first-match
refuse: rule-name-and-form, reformulate
shell-hook: unchanged, no-duplicate
mcp-tools: unchanged
source: SB-15, sweagent tools.py:29-72, agents.py:946-947
executor-task:action-policy -->

## Три бюджета времени действия (SB-14)

Исполнитель считает время каждого действия по трём бюджетам. Команда получает 30 секунд, установка 300 секунд, вся серия действий 1800 секунд. Исполнитель сравнивает время действия с лимитом его вида в одной точке проверки и называет бюджет, который исчерпан. Источник: карточка SB-14, `sweagent/sweagent/tools/tools.py:139-152`, `sweagent/sweagent/agent/agents.py:958-991`.

Три последовательных таймаута команды прерывают сессию с причиной `exit_command_timeout`. Успешная команда обнуляет счётчик. Таймаут установки и исчерпание общего времени останавливают серию с первой проверки, без ожидания третьего случая. Причина остановки всегда называется по бюджету. Исполнитель не читает логи, чтобы её узнать.

Бюджет действия не меняет контракт ожидания. `beacon_wait_result` продолжает возвращать прежний `reason` и код 143. Информационный таймаут не становится блокером main и не вызывает `beacon_ack decision="blocked:not_mine"`. `WaitStopReason` и его коды выхода остаются без изменений.

<!-- executor-task:action-budgets
budgets: command-30s, install-300s, total-1800s
check: single, elapsed-vs-kind-limit
consecutive: 3 -> exit_command_timeout
success: reset
install-total: immediate-stop
reason: name-the-budget, no-log-reading
wait_result: unchanged, code-143
informational: not-main-blocker
source: SB-14, sweagent/tools.py:139-152, agents.py:958-991
executor-task:action-budgets -->

## Временные артефакты и очистка

Снимки, записи, логи и токены живут вне репозитория. Исполнитель не коммитит временные артефакты. Секрет не попадает в diff, тело PR или текст комментария. Токен не передаётся дочернему агенту. Если узкая недостающая возможность требует токен, координатор использует его сам и не показывает значение воркеру.

Очистка вызывается в конце каждого прогона. Исполнитель знает, что очистка в конце каждого прогона обязательна и при досрочном выходе. Временные снимки, записи и логи удаляются по политике хранения. Артефакты живут только столько, сколько разрешает политика. Очистка control adapter выполняется всегда.

Правило не меняет протокол Watch, число MCP-инструментов и текст отчёта. Источник: карточка CP-21, `pstack/automations/benny/FOR_AGENTS.md:34`, `pstack/automations/benny/skills/reproduce-and-fix-issues/SKILL.md:30`, `:122`, `:310`.

<!-- executor-task:artifacts-cleanup
location: outside-repository
no-commit: captures, recordings, logs, tokens
token: narrow-missing-capability-only, not-shown-to-worker, not-passed-to-child
cleanup: every-run, end-of-run, control-adapter, early-exit
retention: policy-limited
protocol-change: none
source: CP-21, benny FOR_AGENTS.md:34, reproduce-and-fix-issues/SKILL.md:30,122,310
executor-task:artifacts-cleanup -->

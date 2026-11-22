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

## Входные данные

| Поле | Источник | Назначение |
| --- | --- | --- |
| cardID | ответ `beacon_task_claim` | что делает исполнитель |
| цель, зависимости, OwnerFiles, режим плана, handoff | ответ `beacon_task_claim` | контекст задачи без поиска по `docs/tasks/` |
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

## Цикл: claim, register, push PR, finish turn

1. **claim.** Получить задачу через `beacon_task_claim`, если оркестратор ещё не выдал её. Сохранить ID, цель, зависимости и OwnerFiles из ответа. Не искать задачу вручную в `docs/tasks/`.
2. **register.** Вызвать `beacon_watch_pr` с `repo: "refusedguy/beacon"`, `pr`, `headSHA`, `role: "executor"` или `"fixer"` и `notify: { adapter: "opencode", target: "<ses_...>" }`. В `target` идёт РЕАЛЬНЫЙ идентификатор сессии из `_meta["ai.opencode/sessionID"]` или явно переданный `ses_...`. Значение `<cardID>` в `target` запрещено: wake будет доставлен не туда. Повторный вызов с теми же параметрами идемпотентен. Один агент может держать несколько Watch.
3. **push PR.** Перед `git worktree add ... origin/main` обязателен `git fetch origin main`: база берётся из свежего `origin/main`. Premise-check через `git merge-base --is-ancestor` делать только после fetch. Устаревшая база даёт ложный premise-check и конфликты. Сделать изменения в своём worktree и своей ветке от `origin/main`. Прямые коммиты в `main` запрещены. Локально запускать только `gofmt -w <изменённые_файлы>`. Перед пушем запускать синтаксический предпроход по изменённым файлам и не пушить при ошибке. Полные проверки прогоняет GitHub Actions CI на PR, не запускать `go test ./...`, `go build ./...` и `beacon gates` локально. Запушить ветку и открыть PR в `main` командой `gh pr create --base main --head <ветка>`. В отчёте указать номер PR, headSHA и список файлов.
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

## Действие после retry

Вердикт `retry` требует классификации до конца хода. Исполнитель читает `failedChecks` через `beacon_get_result` и сверяет их со своим diff. Отвечать `waiting, <watchID>` сразу после retry запрещено. Такой ответ оставляет задачу без владельца и повторяет инцидент 334, где агент дважды ответил waiting и не написал blocked.

После классификации исполнитель делает одно из двух. Если упавшая проверка лежит в его diff, он исправляет ветку и пушит новый headSHA. Если причина вне его diff, он вызывает `beacon_ack` с `decision="blocked:not_mine"` и непустой причиной. Действие выполнено до фразы ожидания, и только после неё исполнитель завершает ход.

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

Блок `executor-task:test-output` фиксирует поля вывода упавшего теста, лимит 2 КБ, флаг обрезки, ссылку на полный лог и сохранение классификации и вердиктов.

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

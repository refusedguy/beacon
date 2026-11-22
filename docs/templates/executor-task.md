# Шаблон задания исполнителя

Шаблон задания для агента-исполнителя или фикера в цикле Beacon. Исполнитель регистрирует Watch на реальную сессию, сдаёт PR через ветку и worktree, завершает ход фиксированной фразой и просыпается только когда мерж невозможен. Сон означает завершение хода, а не опрос в цикле. Основа: WORKER-PROMPT §4.7, ADR 0004, ADR 0019, ADR 0021, ADR 0072, ADR 0258.

## Карта жизненного цикла карточки

Карточка Beacon проходит семь фаз. У каждой фазы есть вход, выход, владелец и артефакт. Фаза без артефакта проваливает карточку.

| Фаза | Вход | Выход | Владелец | Артефакт |
| --- | --- | --- | --- | --- |
| explore | карточка и вопрос владельца | найденные факты | исследователь | research-файл |
| plan | research-файл | шаги и зависимости | исполнитель | PLAN.md |
| improve | готовый план | пробелы и правки плана | оркестратор | запись ревью |
| implement | просмотренный план | изменения в ветке | исполнитель | diff в PR |
| verify | изменения в PR | результат проверок | CI | отчёт проверок |
| commit | пройденные проверки | коммит со ссылками | исполнитель | коммит с карточкой и ADR |
| rules-check | изменённые файлы | нарушения правил | gate | отчёт проверки правил |

Карта повторяет фазы Harbor workflow-lifecycle и шесть фаз цикла aif-loop (ADR 0229, ADR 0241). Beacon не переносит код Harbor и использует только перечень фаз.

<!-- executor-task:lifecycle
phases: explore, plan, improve, implement, verify, commit, rules-check
fields: input, output, owner, artifact
no-artifact: fail
executor-task:lifecycle -->

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
3. **push PR.** Перед `git worktree add ... origin/main` обязателен `git fetch origin main`: база берётся из свежего `origin/main`. Premise-check через `git merge-base --is-ancestor` делать только после fetch. Устаревшая база даёт ложный premise-check и конфликты. Сделать изменения в своём worktree и своей ветке от `origin/main`. Прямые коммиты в `main` запрещены. Локально запускать только `gofmt -w <изменённые_файлы>`. Полные проверки прогоняет GitHub Actions CI на PR, не запускать `go test ./...`, `go build ./...` и `beacon gates` локально. Запушить ветку и открыть PR в `main` командой `gh pr create --base main --head <ветка>`. В отчёте указать номер PR, headSHA и список файлов.
4. **finish turn.** Завершить ход с отчётом ровно `waiting, <watchID>`, подставив сохранённый `watchID`. Не опрашивать состояние в цикле, не крутить `gh pr checks --watch`, не использовать `sleep` и не ждать вручную. Дальше агент спит до вердикта, который разрешает пробуждение.

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

На вердикте `retry` исполнитель не чинит код вслепую. Он классифицирует причину, сравнив `failedChecks` со своим diff, и только потом решает, что делать (ADR 0072, ADR 0155). Классификация уходит wake'ом оркестратору, а не вопросом владельцу.

<!-- executor-task:triage
source: beacon_get_result.failedChecks
fields: name, conclusion, detailsUrl, required, flakyMatched
compare: git diff <baseRef>..HEAD
own-diff: fix-branch-and-push-new-headSHA
foreign-or-flaky: blocked:not_mine
unknown: report-no-own-changes
fix-main: forbidden
wake: orchestrator
executor-task:triage -->

Процедура:

1. Прочитать снимок `beacon_get_result`. В ответе есть `failedChecks` с полями `name`, `conclusion`, `detailsUrl`, `required` и `flakyMatched`. Полные логи не нужны.
2. Сверить `failedChecks` со своим diff: `git status --short` и `git diff <baseRef>..HEAD` в своём worktree. Проверка считается твоей, когда её причина лежит в файлах, которые ты менял в этой ветке.
3. Выбрать причину и отослать wake подписчику `role=orchestrator` на том же Watch.
   - Причина вне твоего diff или красный `main` на последнем коммите: флейк или чужая поломка, `decision="blocked:not_mine"`.
   - Причина в твоём diff: твоя ошибка. Исправить ветку и запушить новый headSHA.
   - Причину нельзя отнести ни к тому, ни к другому: сообщить, что изменений моих нет.
4. Завершить ход. Wake оркестратору не означает готовность PR. Мерж делает только оркестратор после своего `beacon_get_result` с `verdict=ready`.

Не чинить `main` самому, не пушить в `main` и не править файлы вне своего worktree.

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

## Запреты

- Не запускать опрос в цикле вместо завершения хода.
- Не выдавать готовность по idle-отчёту и по вердикту `ready` без решения Beacon или оркестратора.
- Не поручать Beacon команды, которые выполняет агент. Beacon только детектирует состояние и шлёт указатель на запись.
- Не читать внутренние логи и базы агента как источник результата (ADR 0027).
- Не закрывать чужие карточки в несвязанных PR.
- Не запускать `batch-apply` без явной команды владельца.
- Не отправлять PR с изменением кода без обновления документации в том же PR.
- Не искать задачу вручную в `docs/tasks/` вместо `beacon_task_claim`.
- Не добавлять зависимость, которая замыкает цикл в графе.
- Не переводить карточку в `done` вручную: это делает Beacon при `merged`.
- Не открывать PR-дубль, когда требование карточки уже закрыто другим изменением.
- Не возвращать блокер без доказательства: SHA, код `merge-base`, PR или ссылка на решение владельца.

## Самопроверка шаблона

Тест шаблона лежит в `tests/adapter/executor-template.test.js` и читает этот файл через `node:fs`. Он разбирает маркеры и проверяет четыре условия карточки: `ready` и `watching` не будят, `keep_working`, `rebase` и `retry` будят, текст `waiting, watchID` присутствует, у Beacon нет команды от имени агента. Дополнительно проверяется, что re-run принадлежит агенту и TTL отсутствует.

Блок `executor-task:claim` фиксирует, что задача приходит через `beacon_task_claim`, цикл отвергается, при `PERSIST_FAILED` оркестратор получает `blocked:not_mine`, а карточку в `done` переводит Beacon при мерже.

CI подхватывает тест шагом `Test JS adapters` в `.github/workflows/ci.yml`: `find tests/adapter -maxdepth 1 -name '*.test.js' | xargs node --test`. Тест использует только встроенные модули Node.

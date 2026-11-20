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
| cardID | карточка задачи в `docs/tasks/` | что делает исполнитель |
| sessionID | `_meta["ai.opencode/sessionID"]` или явный `notify.target: "<ses_...>"` | адрес живой сессии для wake |
| repo, pr, headSHA, baseRef | факт сдачи PR | параметры Watch |
| watchID | ответ `beacon_watch_pr` | ключ ожидания и отчёта |
| verdict | `beacon_get_result` | решение о пробуждении |

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

## Цикл: register, push PR, finish turn

1. **register.** Вызвать `beacon_watch_pr` с `repo: "refusedguy/beacon"`, `pr`, `headSHA`, `role: "executor"` или `"fixer"` и `notify: { adapter: "opencode", target: "<ses_...>" }`. В `target` идёт РЕАЛЬНЫЙ идентификатор сессии из `_meta["ai.opencode/sessionID"]` или явно переданный `ses_...`. Значение `<cardID>` в `target` запрещено: wake будет доставлен не туда. Повторный вызов с теми же параметрами идемпотентен. Один агент может держать несколько Watch.
2. **push PR.** Сделать изменения в своём worktree и своей ветке от `origin/main`. Прямые коммиты в `main` запрещены. Локально запускать только `gofmt -w <изменённые_файлы>`. Полные проверки прогоняет GitHub Actions CI на PR, не запускать `go test ./...`, `go build ./...` и `beacon gates` локально. Запушить ветку и открыть PR в `main` командой `gh pr create --base main --head <ветка>`. В отчёте указать номер PR, headSHA и список файлов.
3. **finish turn.** Завершить ход с отчётом ровно `waiting, <watchID>`, подставив сохранённый `watchID`. Не опрашивать состояние в цикле, не крутить `gh pr checks --watch`, не использовать `sleep` и не ждать вручную. Дальше агент спит до вердикта, который разрешает пробуждение.

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

## Самопроверка шаблона

Тест шаблона лежит в `tests/adapter/executor-template.test.js` и читает этот файл через `node:fs`. Он разбирает маркеры и проверяет четыре условия карточки: `ready` и `watching` не будят, `keep_working`, `rebase` и `retry` будят, текст `waiting, watchID` присутствует, у Beacon нет команды от имени агента. Дополнительно проверяется, что re-run принадлежит агенту и TTL отсутствует.

CI подхватывает тест шагом `Test JS adapters` в `.github/workflows/ci.yml`: `find tests/adapter -maxdepth 1 -name '*.test.js' | xargs node --test`. Тест использует только встроенные модули Node.

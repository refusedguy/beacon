# Замкнутый контур "PR merged -> задача done -> уведомление"

Дата: 2026-11-21. Область: `beacon-impl`. Режим: только чтение. Код не менялся, `go test`, `go build` и `beacon gates` не запускались, инструменты `tools.beacon` не вызывались. Первичные источники: официальная документация GitHub, Mergify, Renovate, Temporal, LangChain, Anthropic и OpenCode.

## Методика

Сначала я прочитал локальные правила и проектные документы Beacon: `AGENTS.md`, `docs/adrs/0155-blocked-watch.md`, `docs/evals/w15-closure-reasons.md` и `docs/research/watch-lifecycle-audit.md`. Затем собрал поведение семи систем по четырём вопросам: что запускает завершение, где помечается задача, кто получает уведомление, что происходит при ошибке или блокере. Отдельно собрал случай "задача закрыта без PR".

Документация GitHub по автоматизации Projects и по закрытию issue с причиной вернула 404 на опробованных адресах. Поэтому утверждения об автоматическом переносе карточки Projects в Done и о кнопке "not planned" здесь не приводятся как проверенные. Проверенный факт только один: merge linked PR в default branch закрывает issue.

## Пять находок с источниками

1. GitHub закрывает issue по ключевому слову в описании PR или коммите, но только когда PR targets default branch. Для другой ветви слова игнорируются, связь не создаётся и merge ни на что не влияет. Источник: https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/linking-a-pull-request-to-an-issue

2. Mergify оценивает состояние PR, а не отдельные события, и использует edge triggering. Неудачное действие считается выполненным. Пока правило продолжает совпадать, повтора не будет. Повтор требует `@mergifyio refresh` или ручного rerun check run. Источник: https://docs.mergify.com/workflow/

3. Temporal разделяет Signal и Update. Signal это асинхронная запись, ответа и ошибки отправитель не ждёт. Update это синхронная отслеживаемая запись с результатом или ошибкой, и её можно проверить до принятия в историю. Источник: https://docs.temporal.io/encyclopedia/workflow-message-passing

4. LangGraph ставит `interrupt()` в узел и сохраняет состояние через checkpointer. Возобновление идёт через `Command(resume=...)` с тем же `thread_id`. Узел при возобновлении стартует с начала, поэтому побочные эффекты до `interrupt()` должны быть идемпотентными. Источник: https://docs.langchain.com/oss/python/langgraph/interrupts

5. В Claude Code события `TaskCompleted` и `TeammateIdle` не только сообщают о завершении. Exit code 2 или `{"decision":"block","reason":"..."}` оставляет задачу открытой и возвращает причину модели. `{"continue":false,"stopReason":"..."}` даёт тиммейту уйти в idle, оставив задачу in progress. Отдельного события "закрыть как не делаем" в документации нет. Источник: https://code.claude.com/docs/en/hooks

## Разбор систем

| Система | Триггер завершения | Где помечается задача | Кто получает уведомление | Ошибка или блокер |
| --- | --- | --- | --- | --- |
| GitHub Actions + linked issues | Merge PR в default branch | Issue переводится в closed; связь по ключевым словам `close`, `fix`, `resolve` в описании PR или коммите | Watchers issue и PR через стандартные уведомления GitHub | Ключевые слова игнорируются для не-default branch. Ручная связь до десяти issue на PR. Ключевое слово в коммите закрывает issue без списка linked PR |
| Mergify | Правило `pull_request_rules` совпало с текущим состоянием PR, затем `merge`, `queue`, `label` или `comment` | Состояние PR на GitHub. Внутренней задачи Mergify нет. Дополнительно доступны label, comment, backport | GitHub и участники PR через комментарии правила. Агентного wake нет | Mergify сам добавляет условия: не draft, не closed, без конфликта, branch protection, merge protections, зависимости `Depends-On:`, merge date. Неразрешённая зависимость блокирует merge, а не ждёт. Неудачное edge-действие не повторяется без refresh |
| Dependabot и Renovate | Найдено обновление, создан PR или branch, пройдены обязательные проверки, затем auto-merge | PR merged или commit попадает прямо в base. Renovate режим `automergeType=branch` убирает PR вообще | Renovate не ставит assignees и reviewers на automerge PR при создании. При падении проверок добавляет их. `assignAutomerge` заставляет добавлять всегда | Renovate ждёт около двух часов и мержит не более одной ветви на target branch за запуск. После merge перебазирует остальные. Dependabot прекращает rebase через 30 дней без merge и реагирует на comment commands |
| Temporal | Актор посылает Signal или Update работающему workflow. Update может вернуть результат или ошибку | Состояние workflow execution и Event History. Отдельной карточки задачи нет | Отправитель Update ждёт ответа. Отправитель Signal нет. Query читает состояние без записи в историю | Update проверяется до принятия. Signal не даёт ошибки отправителю. История событий остаётся durable журналом |
| LangGraph | `interrupt()` в узле графа, затем внешний вызов `Command(resume=...)` | Checkpoint с состоянием графа. `thread_id` это постоянный указатель | Вызывающий сам крутит цикл `stream_events`, смотрит `stream.interrupted` и подставляет ответ | Граф ждёт бесконечно. `response_schema` проверяет payload. Неверный вызов даёт `ValidationError`, повтор идёт с тем же `thread_id`. Узел стартует с начала |
| Claude Code хуки | `TaskCompleted`, `SubagentStop`, `TeammateIdle`, `Stop`, `TaskCreated` | Статус задачи через TaskUpdate. Хук может не дать задаче закрыться | Хук получает `task_id`, `task_subject`, `teammate_name`. Решение возвращается exit code или JSON | Exit 2 оставляет задачу открытой. Для Stop и SubagentStop есть предел в восемь продолжений подряд. Если stderr говорит об отсутствии файла, отказ считается неблокирующим. `background_tasks` и `session_crons` позволяют отличить "готово" от "ждёт фоновую работу" |
| OpenCode subagent | Дочерняя сессия уходит в idle. Родитель получает report при idle и после каждого последующего раунда работы | Состояние дочерней сессии. Отдельной карточки с статусом в документации агента нет | Report родителю. Разбудить сессию можно через `opencode.sessions.prompt({ sessionID, text })` | Права родителя на `subagent` определяют, кого он может запустить. Хук `Stop` внутри subagent превращается в `SubagentStop`. Встроенного close reason у задачи нет |

Источники для строк таблицы, помимо пяти находок выше:
- Renovate: https://docs.renovatebot.com/key-concepts/automerge/
- Dependabot: https://docs.github.com/en/code-security/dependabot/working-with-dependabot/managing-pull-requests-for-dependency-updates
- Mergify merge action: https://docs.mergify.com/workflow/actions/merge
- OpenCode agents: https://opencode.ai/docs/agents/
- OpenCode SDK: https://opencode.ai/docs/sdk/

### Случай "задача закрыта без PR"

| Система | Что делает система вместо merge | Отдельный инструмент или событие |
| --- | --- | --- |
| GitHub | Issue закрывается вручную или по ключевому слову в коммите. Ключевое слово в коммите не создаёт linked PR | Причина закрытия как отдельный инструмент в прочитанных документах не найдена |
| Mergify | PR, закрытый автором без merge, перестаёт совпадать с правилами. Уборка идёт через `label` и `comment` | Отдельного close-действия нет. Есть backport, который срабатывает после merge |
| Dependabot | Команды комментария `ignore` и конфиг `ignore` сохраняют решение централизованно. Обновление больше не предлагается | Есть comment commands и поле конфигурации. Это ближайший аналог "won't do", и он не требует PR |
| Renovate | Правила `packageRules` с `enabled: false` и Dependency Dashboard убирают обновление из потока | Есть конфиг и dashboard. Отдельного события закрытия задачи нет |
| Temporal | Причина передаётся в полезной нагрузке Signal или Update | Отдельного close-сигнала в прочитанной документации по message passing нет |
| LangGraph | `Command(resume=...)` несёт любое JSON-значение, включая отказ или дубликат. `response_schema` задаёт форму ответа | Отдельного инструмента нет. Один и тот же resume-канал несёт одобрение и отказ |
| Claude Code | `TaskCreated` может откатить создание дубля. `TaskCompleted` может оставить задачу открытой с причиной | Отдельного события "не делаем" нет. Причина живёт в `reason` и `stopReason` |
| OpenCode | Родитель получает report и сам решает, что делать с результатом | Инструмента закрытия задачи в документации агента нет |

Общий вывод: причина закрытия чаще всего поле внутри уже существующего сообщения или решения, а не отдельный инструмент. Отдельный канал есть только у Dependabot и Renovate: ignore и конфиг хранятся вне PR.

## Сопоставление с цепочкой Beacon

Текущая цепочка по `AGENTS.md`, ADR 0155 и аудиту lifecycle:

1. `beacon_task_claim` выдаёт карточку и ставит ей `in_progress`.
2. Агент создаёт ветку и PR, вызывает `beacon_watch_pr` с `headSHA` и `notify.target` вида `ses_...`.
3. CI-мост считает вердикт через `EvaluatePR`. `keep_working`, `rebase`, `retry` будят исполнителя. `ready` ведёт к авто-мержу по ADR 0072.
4. `beacon_ack decision="blocked:not_mine"` создаёт `BlockInfo` и `BlockerIncident`, будит оркестратора для FIX-агента. `blocked:resolved` снимает блок.
5. При терминальном вердикте `merged` Beacon переводит связанную карточку в `done` и пишет ссылку на PR. В продакшене связанными считаются только wiring-карточки, см. G1.

Разрывы:

G1. Закрытие карточки привязано к merge PR, и даже эта связь в продакшене неполная. Порт `CardLinks` вызывается только для `VerdictMerged`, но в composition root он привязан к `deferredCardLinks` и закрывает только карточки `WIRING-<pr>`. Обычная карточка из `beacon_task_claim` не имеет строки в `card_links`, поэтому merge PR не меняет её статус. У Beacon нет `beacon_task_close` и нет события "закрыть без PR". Карточка, которую агент взял и бросил, остаётся `in_progress` без срока и причины. Доказательства по строкам кода в `docs/research/task-lifecycle-brainstorm.md`.

G2. При этом в `AGENTS.md` уже есть статус `dropped` с нужным смыслом: дизайн брошен или дублирует существующий код. Не хватает перехода в этот статус и причины. Получается, что состояние есть, а события и инструмента для него нет. Это ближайший аналог "duplicate" и "won't do".

G3. Блокеры без PR не имеют входа в систему. `blocked:not_mine` требует Watch, обязательства, ревизии и снимка упавших проверок CI. Если PR ещё не создан, а блокер внешний, например ждём решение владельца, сломано окружение или накоплен долг по wiring, Watch не существует. Оркестратор не получает структурированного сигнала и не может запланировать работу.

G4. Нет close reason на карточке. В `docs/evals/w15-closure-reasons.md` причины закрытия есть у обязательств, но не у карточек задач. Метрики не отличают merged от duplicate от брошенной задачи.

G5. `beacon_defer_wiring` создаёт карточку `WIRING-<PR>`, то есть задачу без собственного PR. Её жизненный цикл не привязан к закрытию родительского PR и не имеет таймера.

G6. Аудит lifecycle уже фиксирует слабые места, которые унаследует любая схема закрытия: проверка ревизии в `beacon_ack` пропускает ноль и будущие значения; Watch в `blocked` не получает преемника при смене head; повторная регистрация может перезаписать заблокированный Watch. Список с доказательствами в `docs/research/watch-lifecycle-audit.md`, находки 3, 4 и 9.

## Что уже есть без новых инструментов

`beacon task done <id> --summary ... --evidence ...` и `beacon task needs-input <id> --question ...` уже реализованы в `cmd/beacon/task.go`. Команды берут flock, пишут через atomic write, принимают ID вида `ARDH-08` и не меняют контракт MCP. Для `dropped` и `duplicate` команд нет. Разбор по строкам кода в `docs/research/task-lifecycle-brainstorm.md` и `docs/research/mcp-close-tool-brainstorm.md`.

## Три варианта замкнуть контур

| Вариант | Механизм | Риск | Цена |
| --- | --- | --- | --- |
| A. Event-driven по merged PR | Расширить `verdictApplier`: вместе с `merged` обработать `pull_request.closed` с `state_reason`. Писать в карточку `done` или `dropped` и `close_reason=merged/not_planned`. Для `closed_without_merge` добавить отдельный вердикт и wake оркестратору | Не решает случай "PR ещё нет". `state_reason` есть не у всех провайдеров и не у всех self-hosted конфигураций. Терминальные состояния уже обрабатываются с ошибками, см. G6 | Низкая или средняя. Нет нового MCP-инструмента. Меняются domain, applier, store и eval |
| B. Демон сканирует `claimedAt` по TTL | Фоновая метла раз в N минут ищет карточки `in_progress` без живого Watch и без PR старше TTL. Переводит в `dropped` с `close_reason=stale`, пишет эскалацию и будит владельца. TTL для карточек сегодня нет, есть поле `Время` и устаревшие файловые аренды по PID | Эвристика. Долгоживущая задача с редкими коммитами может быть закрыта ошибочно. Нужны lease, heartbeat и проверка GitHub перед закрытием. Гонка с агентом, который только что создал PR | Средняя. Новый цикл, поле close_reason, метрики, конфиг TTL. Инструмент не добавляется |
| C. Новый MCP-инструмент `beacon_task_close` | Явный вызов с `cardID`, `close_reason` из перечисления `merged/duplicate/already_done/wont_do/blocked_external` и необязательным `evidence`. Идемпотентность по `(cardID, close_reason)`, проверка владельца, wake оркестратору. Писать статус и причину в markdown-карточку, затем пересобирать индекс | Доверие: агент сможет закрыть свою задачу без PR. Десятый инструмент ломает побайтный golden `tools/list` на 18212 байт и SHA, бюджет 20 KiB. Статусы `blocked` и `duplicate` требуют правки `cardStatuses` и doc gate. Расширение `beacon_ack` не спасает контракт, потому что меняется его `inputSchema`. ADR 0094 уже отверг такую схему | Высокая. Отдельный ADR, новое имя, фабрика схемы, золотые константы, eval и документация. Стоимость измерена в `docs/research/mcp-close-tool-brainstorm.md` |

### Рекомендация

Сначала использовать уже готовый CLI под оркестратором: `beacon task done` с доказательством и `beacon task needs-input` с вопросом. Он не трогает контракт MCP и закрывает текущие застрявшие карточки без кода.

Дальше идти в A, но сначала починить связь карточки с Watch. Сейчас `CardLinks` закрывает только wiring-карточки, поэтому один разбор `pull_request.closed` сам по себе не переведёт обычную карточку в `done`. C оставить как отдельное решение с новым ADR: ни десятое имя, ни поле в `beacon_ack` не сохраняют побайтный golden `tools/list`. B годится как страховка после проверки живых Watch и worktree, с измерением частоты ложных срабатываний до включения в прод.

Порядок совпадает с рекомендацией в `docs/research/task-lifecycle-brainstorm.md`: сейчас CLI и runbook, потом исправление связи, потом новый инструмент или TTL.

## Что не проверялось

Живой прогон Beacon, MCP-сервера и GitHub webhook не выполнялся. Документация GitHub по Projects automation и по закрытию issue с причиной не открылась, поэтому выводы об автопереходе в Done и о причине "not planned" здесь отсутствуют. Поведение `merged` в части wake агента не прослежено до конкретного кода.

Факты о `CardLinks`, CLI `beacon task done` и побайтном golden `tools/list` взяты из отчётов соседних наблюдателей `docs/research/task-lifecycle-brainstorm.md` и `docs/research/mcp-close-tool-brainstorm.md`. Сам код я не читал и эти строки не проверял. Перечисление close reason в варианте C это предложение, а не существующий контракт.

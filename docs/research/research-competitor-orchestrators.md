# Конкуренты и аналоги оркестраторов агентов

Дата: 2026-11-22. Метод: прямое чтение официальных страниц через `webfetch`. Поиск не использовался, все URL взяты из официальных индексов документации или репозиториев. Код не менялся. `go test`, `go build`, `beacon gates` и `batch-apply` не запускались. Сверка с Beacon сделана по `origin/main` на коммите `2d34645b54f45d84dba3a595350f17f8658759da` точечными `git show` и `git grep`.

Отчёт отвечает на пять вопросов по каждому аналогу: параллельные слоты, изоляция worktree, автоматический мерж и CI, передача контекста между агентами, детект застрявших задач. Фраза «не нашёл» означает отсутствие в прочитанных первичных страницах, а не доказательство отсутствия в продукте.

## Первичные источники

| Аналог | Прочитанные страницы | Дата |
| --- | --- | --- |
| OpenAI Codex CLI | README репозитория, `docs/` через GitHub API, справочник команд CLI, страница `AGENTS.md` | 2026-11-22 |
| Factory.ai Droids | индекс документации и страницы Workstreams, Missions, Automations, Hooks, CVE Remediation | 2026-11-22 |
| Cursor Cloud Agents | индекс документации, страницы Cloud Agents, Capabilities | 2026-11-22 |
| Claude Code | страницы Subagents, Hooks, Agent view | 2026-11-22 |

Ссылки на конкретные страницы собраны в разделе «Источники» в конце.

## Сводная таблица

| Система | Параллельные слоты | Изоляция | Мерж и CI | Контекст | Застрявшие задачи |
| --- | --- | --- | --- | --- | --- |
| Codex CLI | `codex agents` как центр управления чатами, `codex queue` в существующий поток, `codex cloud exec` для задач, `/subagents` | `codex exec --worktree` и `codex fork --worktree` создают управляемый worktree, `/worktree` продолжит чат там же | Авто-мержа в прочитанных документах нет. `codex apply` накладывает последний дифф облачного чата через `git apply`, при конфликте выходит ненулевым кодом | Цепочка `AGENTS.override.md`, `AGENTS.md` и fallback-имён от корня к cwd, лимит 32 KiB, `/recap` и `/compact`, `exec fork` копирует сессию | `/ps` и `/stop` для фоновых терминалов, `codex doctor`, поток `--json`. Явного детекта зависшего агента нет |
| Factory Droids | Workstream: один оркестратор и любое число агентов. Пейс CVE: 2 новых задачи и 5 открытых, 10 и 25, 25 и 100 | Свежий облачный компьютер на агента через Environment, опция worktree для оркестратора, общая машина по умолчанию | Фикс-дroid держит CI зелёным. Мерж только после выбора человека в Tasks tab, принуждение через branch protection. PR-таб показывает проверки и статусы | Общая папка синхронизирует планы, заметки, находки, handoff и инструкции между машинами. Оркестратор пишет каждому агенту бриф, агент не видит диалог оркестратора | Группы Waiting on you, Working, Idle. В CVE прямо сказано, что дройд без ответа не детектится. Missions предлагают ручное вмешательство |
| Cursor Cloud Agents | Параллельных агентов можно запускать сколько угодно. API пулов Machines умеет claim, release, fail и watch pending request | Каждый агент в отдельной VM с репозиторием, зависимостями и секретами. Работа идёт в отдельной ветке, результат пушится в репозиторий | Merge-ready PR и артефакты в описании. Автоисправление CI с условиями пропуска и лимитом 10 попыток. CI-подписка отдаёт результат по коммиту | Подписка будит того же агента follow-up сообщением в той же беседе с полным контекстом. Пачки событий сливаются, агент перечитывает источник | События дашборда `setup_failed`, `pr_creation_failed`, `mcp_auth_error`. Подписка ждёт завершения всех проверок коммита, один зависший check удерживает результат |
| Claude Code | `claude agents` запускает много фоновых сессий, каждая строка это полная беседа. Subagents работают в своём окне контекста | `isolation: worktree` в frontmatter сабагента, временный worktree от ветки по умолчанию, автоудаление без изменений. Хуки `WorktreeCreate` и `WorktreeRemove` | Авто-мержа нет. Agent view показывает ссылку на PR и цвет по статусу проверок и ревью, человек мержит сам | Сабагент возвращает краткий итог. Хуки `SessionStart`, `UserPromptSubmit`, `PreCompact` добавляют контекст. Есть обмен сообщениями между сессиями | Семь состояний строки, в том числе Needs input и Ready for review. Короткая сводка активности от Haiku-класса, обновление не чаще раза в 15 секунд, смена каждые несколько минут |

## OpenAI Codex CLI и режим AGENTS.md

Источники: [CLI reference](https://developers.openai.com/codex/cli/reference.md), [AGENTS.md](https://learn.chatgpt.com/docs/agent-configuration/agents-md.md), [README](https://raw.githubusercontent.com/openai/codex/main/README.md).

Codex ищет инструкции по слоям. Он читает `AGENTS.override.md` в доме, иначе `AGENTS.md`. От корня проекта до текущего каталога он проверяет в каждой директории `AGENTS.override.md`, затем `AGENTS.md`, затем имена из `project_doc_fallback_filenames`. В каждый каталог попадает не больше одного файла. Файлы склеиваются от корня вниз, поэтому ближайший к cwd текст идёт последним. Суммарный лимит `project_doc_max_bytes` равен 32 KiB. Для проверки загруженных источников документ предлагает лог TUI или `session-*.jsonl`.

Параллельные слоты живут вокруг сессий. `codex agents` открывает центр управления чатами на общем сервере. `codex queue --thread SESSION_ID --message` кладёт текст в существующую сессию, флаг `--resume` поднимает её, если она выгружена. `codex cloud exec` отправляет задачу в облако, `codex cloud list --json` отдаёт список для скриптов. Страница `codex exec` описывает `--json` с событиями по каждому изменению состояния, `resume`, `fork` и `--worktree`.

Изоляция через worktree встроена в CLI. `codex exec --worktree` создаёт управляемый git worktree для нового или форкнутого запуска. Команда требует локального исполнения и несовместима с `resume`, `review`, `--ephemeral` и `--ignore-user-config`. `/worktree` в TUI создаёт worktree и начинает или продолжает чат там. Архивация сессии захватывает её сабагентов и потомков.

Авто-мержа как шага тулзы в прочитанных документах нет. `codex apply` берёт последний дифф из облачного чата и накладывает его через `git apply`, при конфликте выходит с ошибкой. `codex review` работает неинтерактивно и принимает одну цель проверки. Проверка `/approve` разрешает одну повторную попытку действия, которое отклонил автоматический ревьюер.

Передача контекста строится на файлах инструкций и копировании сессий. `codex exec fork <SESSION_ID>` копирует сохранённую беседу в новую, `/recap` делает сводку без сжатия, `/compact` освобождает место. `/goal` держит цель, прикреплённую к активному чату, с паузой и сбросом. Описание цели до 4000 символов, длинные детали выносятся в файл.

Для застрявших задач документ даёт `/ps` и `/stop` для фоновых терминалов, `codex doctor` для диагностики установки и `/status` для модели, политики одобрений и остатка контекста. Явного правила «агент молчит N минут, значит блок» в прочитанных страницах нет.

## Factory.ai Droids

Источники: [Workstreams](https://docs.factory.com/workstreams/overview.md), [Steer](https://docs.factory.com/workstreams/steer.md), [Machines](https://docs.factory.com/workstreams/machines.md), [Shared folder](https://docs.factory.com/workstreams/shared-folder.md), [Pull requests](https://docs.factory.com/workstreams/pull-requests.md), [CVE Remediation](https://docs.factory.com/automations/cve-remediation.md), [Hooks](https://docs.factory.com/harness/hooks.md), [Missions](https://docs.factory.com/missions/overview.md).

Workstream делит большую работу между оркестратором и произвольным числом агентов. Оркестратор разбивает цель на области и даёт каждому агенту бриф, потому что агент не видит переписку с оркестратором. Агент сообщает о готовности, блокере или находке, которая меняет план. Вкладка Agents показывает три группы: Waiting on you, Working и Idle. Тот же агент получает новую работу внутри workstream, новый workstream для смежной задачи не нужен.

Пейс параллельных задач описан в CVE Remediation как таблица. Trial создаёт 2 новых задачи за опрос и держит не больше 5 открытых. Standard даёт 10 и 25. Fast даёт 25 и 100. Опрос берёт сначала Findings из каталога CISA KEV, затем по тяжести, затем по возрасту. Достигнутый лимит переносит остальные находки в следующий опрос. Один fix droid идёт за одну задачу от триажа до мержа.

Изоляция выбирается машиной. Environment создаёт свежий облачный компьютер для оркестратора и для каждого агента. Droid Computer может быть общим для всех агентов. Локальная машина доступна только в десктопном приложении, и работа останавливается при выходе. Для оркестратора есть флаг Run in a worktree. Remote delegation по умолчанию включена и разносит агентов по машинам. Описание машины читает оркестратор, поэтому туда пишут, какие репозитории и инструменты на ней есть.

Мерж остаётся решением человека. Fix droid ведёт PR, следит за проверками и чинит только failures, вызванные его изменением. Когда проверки зелёные, задача ждёт выбора Merge PR в Tasks tab. Документ прямо предупреждает, что Factory не принуждает одобрение, а branch protection с обязательными ревью и проверками является реальным контролем. Вкладка PRs группирует PR по Open, Draft, Closed и Merged и показывает проверки со счётчиками комментариев и тредов.

Общая папка работает как носитель контекста. Оркестратор и все агенты читают и пишут одни и те же планы, заметки, находки, handoff, отчёты и результаты. Папка синхронизируется между всеми машинами workstream. Код остаётся в репозитории и уходит PR. В CVE-потоке в этой папке лежат настройки listener, инструкции fix droid и список задач. Документ по безопасности замечает, что любой, кто может писать в папку, меняет поведение агентов.

Детект застрявших задач у Factory слабее, чем изоляция. В CVE Remediation есть прямое ограничение: дройд, который перестал отвечать, не детектится. Если задача долго висит под Agents working, человек открывает сессию через Watch session или спрашивает оркестратора. Missions добавляет сценарии «миссия замерла», «воркер долго сидит на пункте», «застрял на майлстоуне» и предупреждение о лимите ретраев. Все они требуют вмешательства человека.

Хуки Droid работают на событиях `PreToolUse`, `PostToolUse`, `Stop`, `SubagentStop`, `SessionStart`, `SessionEnd`, `PreCompact`, `UserPromptSubmit` и `Notification`. Важная деталь для воспроизведения: hook с `onFailure: block` превращает ошибку валидации или таймаут в блокировку события. По умолчанию невалидный JSON hook fails open, поэтому политический hook нужно ставить на block явно.

## Cursor Cloud Agents

Источники: [Cloud Agents](https://cursor.com/docs/cloud-agent.md), [Capabilities](https://cursor.com/docs/cloud-agent/capabilities.md), [индекс документации](https://cursor.com/docs/llms.txt).

Параллелизм не ограничен числом агентов. Документ говорит, что можно запускать сколько угодно агентов одновременно без подключённого локального компьютера. Индекс документации описывает API пулов Machines с операциями claim pending request, watch pending pool requests, release a claim и fail a claim. Это явная модель очереди воркеров, а не только список чатов.

Изоляция это отдельная VM на агента с полным окружением разработки. Агент клонирует репозиторий, получает зависимости, секреты и сеть, работает в отдельной ветке и пушит изменения для handoff. Поддерживаются мульти-репозиторные окружения, когда задача касается фронтенда, бэкенда и инфраструктуры. Cursor управляет provision, изоляцией, snapshot, запуском, артефактами и ёмкостью.

Мерж и CI закрыты между собой. Агент производит merge-ready PR и прикладывает к нему артефакты: скриншоты, видео и логи. Включение настройки Allow posting artifacts to GitHub кладёт артефакты прямо в описание PR по длинным не угадываемым URL. Автоисправление CI работает только с GitHub Actions и пропускает случай, когда человек запушил новый коммит, когда агенту отправили follow-up, когда та же проверка уже красная на базовом коммите, и когда PR получил 10 follow-up по CI. Настройку можно выключить глобально или комментарием `@cursor autofix off` в конкретном PR.

Подписки продолжают ту же беседу. Агент подписывается на события GitHub, Slack, Linear или таймер, заканчивает ход и просыпается, когда приходит совпадение. Событие приходит follow-up сообщением, поэтому контекст беседы сохраняется. Пачки событий, пришедших близко по времени, сливаются в одно пробуждение, после чего агент перечитывает источник. Подписка живёт не больше 180 дней. CI-подписка ждёт завершения всех проверок коммита и отдаёт один результат: успех или список упавших проверок. Документ советует завершать проверки статусом `action_required` вместо долгого pending, чтобы результат пришёл, а мерж остался под защитой branch protection.

Детект застрявших задач строится на событиях и диагностике. Встроенный Cursor Cloud MCP отдаёт `run-info`, `get-events`, `environment-info`, `list-cloud-agents`, setup-логи и diff metadata. Известные события: `setup_started`, `setup_completed`, `setup_failed`, `pr_created`, `pr_creation_failed`, `artifact_created` и `mcp_auth_error`. Один pending check удерживает весь commit-wide результат. Прямого правила «агент молчит и считается зависшим» в прочитанных страницах нет.

## Claude Code subagents и хуки

Источники: [Subagents](https://code.claude.com/docs/en/sub-agents.md), [Hooks](https://code.claude.com/docs/en/hooks.md), [Agent view](https://code.claude.com/docs/en/agent-view.md).

Сабагент живёт в отдельном окне контекста с собственной системной подсказкой, набором инструментов и правами. Встроенные Explore и Plan работают только на чтение, General-purpose получает почти все инструменты. Пользовательские агенты задаются Markdown с frontmatter, где есть `tools`, `disallowedTools`, `model`, `hooks`, `mcpServers`, `maxTurns`, `memory`, `effort` и `isolation`. Суммарное описание агентов, кроме встроенных, превышающий 15000 токенов, вызывает предупреждение при старте.

Изоляция worktree задаётся полем `isolation: worktree`. Агент получает временный git worktree от ветки по умолчанию, а не от HEAD родителя. Если изменений нет, worktree удаляется автоматически. Claude Code проверяет не только рабочую директорию, но и текст команды: блокирует перенаправление git в основной checkout и отказывает в команде, если не может доказать из текста, что git остаётся в worktree. Хуки `WorktreeCreate` и `WorktreeRemove` заменяют поведение по умолчанию.

Параллельные сессии живут в agent view. Каждый промпт в поле запускает новую фоновую сессию, `/bg` переводит текущую беседу в фон, `/fork` копирует её. Надпись внизу считает сессии, ждущие ввода, и показывает finishing count. Subagents и teammates отдельными строками не отображаются. Сессии сохраняются на диске, supervisor переподключается после сна машины и перезапускает процесс, который перестал отвечать.

Мерж не автоматизирован. Agent view показывает ссылку `#1234` для GitHub PR или `!1234` для GitLab MR и красит её по статусу: жёлтый для проверок и ревью, зелёный для зелёных проверок без блокирующего ревью, фиолетовый для merged, серый для draft и закрытых. Claude Code находит PR по выводу команд `gh` или по ветке через `gh pr view`, повторяя поиск до пяти раз после пша. Решение о мерже принимает человек.

Контекст передаётся компактным итогом и хуками. Сабагент возвращает в родителя summary, а не весь свой transcript. Хуки `SessionStart` и `UserPromptSubmit` добавляют контекст, `PreCompact` и `PostCompact` работают вокруг сжатия. Для сабагента хуки `Stop` превращаются в `SubagentStop`. Поле `once` в skill frontmatter убирает hook после первого успешного срабатывания.

Детект застрявших задач в agent view самый подробный из рассмотренных. Строка имеет состояния Working, Needs input, Idle, Completed, Failed и Stopped, а группа Ready for review отделяет PR с открытым ревью или красными проверками. Однострочную сводку пишет модель класса Haiku. Пока сессия работает, текст обновляется не чаще раза в 15 секунд из собственного вывода сессии без запроса к модели, а раз в несколько минут модель переписывает сводку заново. В peek panel показывается время ожидания ответа, например `waiting 3m`. События `Notification` с типами `agent_needs_input` и `agent_completed` уведомляют без открытого терминала.

## Сверка с Beacon по origin/main

Уже есть, отдельные карточки не нужны:

| Практика | Где лежит |
| --- | --- |
| Один агент, один worktree, одна ветка, один набор OwnerFiles | `docs/adrs/0021-worktree-isolation.md` |
| Бюджет слотов оркестратора и лимиты фан-аута | `internal/domain/orchestrator.go`, `internal/domain/fanout.go` с лимитами 50, 50 и 200 и TTL Handle 30 минут |
| Авто-мерж по `verdict=ready`, исключения политики | `docs/adrs/0072-autonomous-merge-loop.md` |
| Классификация красного, сравнение с базовой веткой, ребейз, wake | `internal/domain/verdict.go`, строки 205-220 |
| Бюджет пробуждений `DefaultMaxWakesPerWatch = 6` и retry budget | `internal/domain/types.go`, `internal/domain/evaluate_pr.go` |
| Поколение claim защищает от stale owner после resolve | `internal/app/orchestrator/claim.go` |
| Состояние Watch, typed attention, карта карточки и журнал событий | `internal/domain/attention.go`, `internal/app/tasks/card_monitor.go`, `internal/infra/cibridge/bridge.go` |
| Сессия в состояниях idle, retry или busy | `cmd/beacon/watch.go`, строка 87 |
| Реестр адаптеров доставки wake и идемпотентность по `(watchID, revision)` | `docs/adrs/0072-autonomous-merge-loop.md`, `docs/adrs/0028` |
| Карточки в git как источник истины, handoff в ответе claim | `AGENTS.md`, разделы «Задачи» и «Режимы плана» |

Пять практик, которых в прочитанном `origin/main` нет. Все пять заведены карточками `CO-01` до `CO-05` в `docs/tasks/`.

1. Подписки на внешние события с возвратом в ту же беседу и слиянием пачек. В Beacon подписка это подписчик Watch на пару `(repo, pr, headSHA)`, а wake приходит по вердикту CI. `git grep` по шаблону `pull_request_review|review_comment|review thread` по `docs`, `internal` и `cmd` дал 0 совпадений. Нет подписок на комментарии ревью, ответы в Slack или состояние issue, нет срока жизни подписки и окна слияния на уровне источника. Карточка `CO-01`.

2. Предохранители автоисправления CI по автору коммита, факту вмешательства и воле владельца PR. Beacon уже сравнивает упавшие проверки с базовой веткой и имеет бюджет wake, но `git grep` по `autofix|auto-fix` дал 0 совпадений. Нет правила «не чинить после human push», нет подавления после follow-up и нет комментария в PR для отключения автофикса. Карточка `CO-02`.

3. Общая папка артефактов и инструкций между всеми агентами и машинами. В Beacon handoff передаётся в ответе claim и в тексте wake, карточки лежат в git. `git grep` по `shared folder|sharedfolder` дал 0 совпадений. Нет синхронизируемого пространства, куда агенты складывают планы, находки и handoff и откуда читают инструкции для своего оркестратора. Карточка `CO-03`.

4. Переиспользование простаивающего агента внутри одной волны. `AGENTS.md` в строке 123 предписывает один spawn на один сигнал. `git grep` по `reuse.*agent|idle agent` дал 0 содержательных совпадений, только имя теста про повторное использование worktree. Нет пула idle-сессий и правила «сначала верни работу агенту, который уже знает код, потом создай нового». Карточка `CO-04`.

5. Короткая сводка активности каждой сессии для детекта застрявших задач. Beacon показывает состояние по вердикту Watch и читает логи через `beacon logs`, а агент считается готовым только по вердикту, не по idle. `git grep` по `row summar|status summar|stall` дал 0 совпадений. Нет одной строки с естественным языком, которая обновляется по мере появления вывода сессии и позволяет заметить зависшего агента без чтения transcript. Карточка `CO-05`.

## Чего не нашёл

Codex, Cursor и Claude Code в прочитанных документах не описывают авто-мерж как действие тулзы. Cursor называет PR merge-ready и показывает артефакты, но решение остаётся за человеком. Claude Code показывает цвет статуса PR и ожидает ручного мержа. Codex накладывает дифф через `codex apply` и не мержит сам. Только Factory доводит задачу до мержа и то после явного выбора человека, а принуждает branch protection.

Factory честно называет свой разрыв в детекте: fix droid без ответа не обнаруживается. Cursor أيضًا не публикует правило тишины в прочитанных страницах, но даёт события setup и PR creation и диагностику через Cloud MCP. Самый готовый механизм сводок активности у Claude Code, и он стоит денег на модель класса Haiku.

Codex задаёт лимит 32 KiB на инструкции и показывает аудит загруженных источников. У Beacon есть стабильный ключ `beacon/survival-rules` и hook на compact, но нет многослойного поиска с override и печатного списка источников. Эта идея осталась вне карточек, потому что пересекается с уже принятыми решениями о выживании после сжатия.

## Источники

OpenAI:

- <https://raw.githubusercontent.com/openai/codex/main/README.md>
- <https://developers.openai.com/codex/cli/reference.md>
- <https://learn.chatgpt.com/docs/agent-configuration/agents-md.md>
- <https://api.github.com/repos/openai/codex/contents/docs>

Factory.ai:

- <https://docs.factory.com/welcome/index.md>
- <https://docs.factory.com/workstreams/overview.md>
- <https://docs.factory.com/workstreams/steer.md>
- <https://docs.factory.com/workstreams/machines.md>
- <https://docs.factory.com/workstreams/shared-folder.md>
- <https://docs.factory.com/workstreams/pull-requests.md>
- <https://docs.factory.com/automations/overview.md>
- <https://docs.factory.com/automations/cve-remediation.md>
- <https://docs.factory.com/missions/overview.md>
- <https://docs.factory.com/missions/running-cli.md>
- <https://docs.factory.com/harness/hooks.md>
- <https://docs.factory.com/llms.txt>

Cursor:

- <https://cursor.com/docs/cloud-agent.md>
- <https://cursor.com/docs/cloud-agent/capabilities.md>
- <https://cursor.com/docs/llms.txt>

Anthropic:

- <https://code.claude.com/docs/en/sub-agents.md>
- <https://code.claude.com/docs/en/hooks.md>
- <https://code.claude.com/docs/en/agent-view.md>

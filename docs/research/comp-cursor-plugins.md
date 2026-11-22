# Конкурентный разбор cursor/plugins: полный клон, а не только skills

Дата: 2026-11-22. Код beacon не менялся. `go test`, `go build`, гейты и `batch-apply` не запускались. Здесь описание структуры и вывод по прочитанным файлам, не измерение.

Источник: полный клон `https://github.com/cursor/plugins`, `git clone --depth 1`, каталог `/tmp/opencode/cursor-plugins-research`, коммит `7a583913eb1b8d1a0ee76753d1a2cf072e7b54f9`. Сторона beacon: `origin/main` `2d34645b54f45d84dba3a595350f17f8658759da`, рабочее дерево `worktrees/comp-cursor-plugins`. Номера строк ниже относятся к этим двум коммитам.

Разделение с соседним исследованием: файл `research-pstack-skills.md` не читался и не повторяется. Здесь разобраны манифесты плагинов, агенты, хуки, правила, паки автоматизаций, playbook'и и исполняемые скрипты. Skills упоминаются только как часть плагина и только там, где без них не понять механизм.

## 1. Что прочитано

| Слой | Объём | Файлы, которые определили вывод |
| --- | --- | --- |
| Манифесты | 21 плагин верхнего уровня, 138 в `third_party/`, две маркетплейс-конфигурации | `.cursor-plugin/marketplace.json:10-80`, `.claude-plugin/marketplace.json:7-17`, `pstack/.cursor-plugin/plugin.json:1-30`, `pstack/plugin.json:1-21` |
| pstack, дерево | 553 файла, 58 `SKILL.md`, 58 в клоне `harness/pstack/claude-code` | `pstack/README.md`, `pstack/docs/guide/01-10`, `pstack/agents/`, `pstack/skills/poteto-mode/playbooks/`, `pstack/skills/poteto-mode/scripts/watch-pr/`, `pstack/automations/benny/` |
| Агенты | 2 в pstack, ещё 9 в `advisor`, `continual-learning`, `create-plugin`, `agent-compatibility`, `cursor-team-kit`, `dyl-stack`, `thermos` | `pstack/agents/poteto-agent.md:1-10`, `pstack/agents/comment-sicko.md:1-32`, `pstack/skills/poteto-mode/worker.md:1-11` |
| Хуки | `advisor`, `continual-learning`, `ralph-loop` | `advisor/hooks/hooks.json:1-27`, `advisor/hooks/lib.sh:1-61`, `advisor/hooks/stop-hook.sh:1-48`, `ralph-loop/hooks/stop-hook.sh:1-86`, `continual-learning/hooks/continual-learning-stop.ts:1-18` |
| Правила | `create-plugin`, `cursor-team-kit`, `third_party/onepay`, `third_party/shopify-store` | `create-plugin/rules/plugin-quality-gates.mdc:1-16`, `cursor-team-kit/rules/typescript-exhaustive-switch.mdc:1-6` |
| Автоматизации | `pstack/automations/benny` целиком | `FOR_AGENTS.md:1-89`, `skills/triage-issue-reports/SKILL.md:1-100`, `skills/reproduce-and-fix-issues/SKILL.md:1-100`, `references/*`, `templates/*` |
| Другие плагины | `orchestrate`, `cursor-team-kit`, `pr-review-canvas`, `thermos`, `ralph-loop`, `advisor`, `dyl-stack`, `grok-voice` | `orchestrate/skills/orchestrate/SKILL.md:1-47`, `ralph-loop/skills/ralph-loop-help/SKILL.md:1-80` |

Каталога `commands/` в репозитории нет. Команды живут как slash-названия skills в frontmatter. `.cursor-plugin/plugin.json:29-30` объявляет только `skills` и `agents`. Хуки и правила подключаются файлами `hooks/hooks.json` и `*.mdc` без записи в манифест. `harness/pstack/claude-code` это отдельная копия того же набора из 58 skills для Claude Code, зарегистрированная в `.claude-plugin/marketplace.json:14-16`.

Слов `deep module`, `grilling` и `VL` в клоне нет. Ближайшие механизмы такие. Deep module это `architect` плюс типы и чистый классификатор в `watch-pr`. Grilling это `interrogate`. Цикл это Ralph stop hook и `autonomous-run`. Ниже разобраны именно они, без выдумывания терминов.

## 2. Пять приёмов с подтверждением по строкам

### CP-01. Вердикт как чистый модуль: типы, приоритет, фейки

В pstack решение о PR вынесено в отдельный исполняемый модуль. `pstack/skills/poteto-mode/scripts/watch-pr/types.ts:1-11` брендирует `PrNumber` и задаёт `NonEmpty<T>`. Дальше `types.ts:145-203` перечисляет `CiState`, `PrSnapshot`, `ReadyPr` и `MergeBlocker` как дискриминированные объединения. Нельзя собрать `ready-pr` без доказанного `CiClean` и пустого списка тредов, см. `types.ts:160-172`. Чистая функция `classifyPr` в `policy.ts:218-236` обходит блокеры в фиксированном порядке: конфликты, треды, CI, гейт (`policy.ts:222-228`), потом pending, потом ready. Различие между падением head и падением base зафиксировано в `policy.ts:33-51` через `hadPreviousPassingCi`. Тесты строятся на фейках: `watch-pr/fakes.test-helper.ts`, `watch-pr/policy.test.ts`, `watch-pr/github.test.ts`. Точка входа занимает шесть строк, `watch-pr/watch-pr:1-6`.

В beacon тип вердикта есть: `internal/domain/types.go:25-44` перечисляет `watching`, `ready`, `rebase`, `retry`, `keep_working`, `timeout`, `merged`, `closed`, `blocked`, `recheck`. Вердикт читается только через `beacon_get_result` (`README.md:58`), а конкретный шаг приходит в `nextAction` (`README.md:83-89`). Разница в том, что у pstack классификация это отдельный модуль со снимком на входе и таблицей тестов на фейках. У beacon решение распределено между `internal/infra/cibridge/bridge.go`, MCP-слоем и правилами цикла в `README.md:76-89`.

Карточка CP-01 предлагает вынести приоритет блокеров в документированную чистую функцию и проверять её на сохранённых снимках. Протокол MCP не меняется.

### CP-02. Контракт воркера одним файлом, загружаемым первой строкой

В pstack правила для сабагента лежат в одном файле и внедряются принудительно. `pstack/skills/poteto-mode/SKILL.md:20` требует, чтобы первой строкой любого kickoff'а была `Read <path>/worker.md before anything else.` Файл `worker.md:1-11` держит весь контракт: оператор важнее автора kickoff'а, планка из kickoff'а не расширяется, блок только по падению на этом head или багу пользователя, `PASS`, `ISSUES`, `BLOCKED`, заголовки тела PR. Агент `poteto-agent.md:3` перед работой читает `SKILL.md` целиком, включая индекс принципов. Подмена на `generalPurpose` пропускает чтение и вызывает дрейф.

У beacon тот же по смыслу контракт размазан. `AGENTS.md:58-73` задаёт выдачу задачи и границы, `.opencode/skills/night-shift/SKILL.md:8-14` даёт пять шагов, `.opencode/skills/red-verdict-triage/SKILL.md:8-19` определяет три причины красного вердикта и запрет чинить main. Правила верны, но нет одного файла, который агент обязан прочесть первой строкой. Отсюда расхождение между `README.md:56-66` с девятью инструментами и `AGENTS.md:115` с десятью, включая `beacon_task_close`.

CP-02 предлагает один `worker.md` в репозитории и первую строку kickoff'а, которая его загружает. Указатель в `AGENTS.md` остаётся указателем, а не вторым источником правил.

### CP-03. Игровой цикл: стоп-хук, предикат выхода, журнал на каждую итерацию

У pstack цикл живёт в хуке, а не в голове агента. `ralph-loop/hooks/stop-hook.sh:16-28` читает frontmatter `iteration`, `max_iterations`, `completion_promise`. Строки `:43-55` проверяют флаг обещания и лимит. Строки `:66-84` инкрементируют счётчик и возвращают `followup_message` с тем же промптом. Хук не опрашивает состояние в цикле. Он реагирует на останов хода. `advisor/hooks/hooks.json:20-24` задаёт `loop_limit: 3`, а `advisor/hooks/stop-hook.sh:24-47` возвращает `followup_message` только при незакрытом маркере правок, когда ответ не заканчивается вопросом к человеку. Содержание цикла описано в `playbooks/autonomous-run.md:3-11`: предикат выхода до первой итерации, wake по событию с резервным heartbeat, минимальное изменение, отбросить изменение без прогресса, плато не является остановом, обещание не ослабляется. График того же цикла в `docs/guide/07-overnight.md:42-56`. Каждая итерация даёт одну строку журнала, см. `07-overnight.md:58-60`. Вердикт `VERIFIED`, `NOT VERIFIED` или `INCONCLUSIVE` описан в `skills/figure-it-out/SKILL.md:36-42`.

У beacon уже есть сильная сторона: Watch по событиям, `waiting, watchID` и явный запрет на опрос CI (`README.md:76-89`, `AGENTS.md:13`). Ревизия меняется только по настоящему событию (`README.md:122`). В самом контракте цикла нет проверяемого предиката выхода и обязательной строки журнала на итерацию. Предикат живёт в задаче и в уме оркестратора.

CP-03 предлагает зафиксировать `exitPredicate` рядом с Watch и писать одну строку решения на каждую ревизию. Wake-механизм beacon трогать не нужно.

### CP-04. Триаж с замороженными координатами и доверенным маркером

Пак автоматизаций `benny` в `pstack/automations/benny/FOR_AGENTS.md:9-24` описывает две связные задачи: классификация репорта и воспроизведение после доверенного маркера. Границы жёсткие. Один ответ в исходном треде, никаких корневых сообщений, сабагенты не получают Slack-креденшелы. `skills/triage-issue-reports/SKILL.md:13-28` запрещает постить до preflight'а, требует неизменяемых координат канала и корневого сообщения и объявляет координатора единственным отправителем. `SKILL.md:30-42` замораживает `SOURCE_CHANNEL_ID` и `SOURCE_THREAD_TS` до любой работы и запрещает подменять их таймстемпом ответа. `skills/reproduce-and-fix-issues/SKILL.md:25-32` требует два воспроизведения через реальный UI и переход в режим проверки, если уже есть PR или коммит. `SKILL.md:56-78` принимает вердикт только от заданного автора, ровно с одним маркером и под замороженным корневым сообщением.

У beacon есть близкая, но более узкая проверка. `.opencode/skills/night-shift/references/comment-rules.md:9-20` разрешает комментарий только при новом факте, `pr-readiness.md:9-11` использует ту же проверку перед мержем. `night-shift/SKILL.md:8-14` предписывает прочесть комментарии и не дублировать сказанное. Нет проверки доверенного автора и точного маркера, нет закрепления координат issue на весь прогон, нет правила «нашёл готовый PR, проверь, а не пиши свой».

CP-04 предлагает добавить эти три проверки в `night-shift` как reference-файлы. Это усиливает существующую проверку нового факта, а не заменяет её.

### CP-05. Ревью, где выживает только доказанная находка

`pstack/skills/arena/SKILL.md:9` открывает todolist из шести фаз до запуска. Rubric из трёх-шести проверяемых критериев готовится до fan-out и не показывается кандидатам (`arena/SKILL.md:16`). Каждый кандидат получает отдельный worktree (`arena/SKILL.md:18`). Судья из другого вендора оценивает по критериям, а не по общему впечатлению (`arena/SKILL.md:28`, `:32`). Лучшие части прошиваются вручную (`arena/SKILL.md:38`), результат проверяется заново (`arena/SKILL.md:44`). `interrogate/SKILL.md:9` заявляет главное: находка выживает только когда прогон её доказал. Бриф в `:19` требует точную команду, её вывод и вывод, которого требует замысел. Проверка в `:23` перезапускает команду, отбрасывает неподтверждённое и объединяет находки по корневой причине. Строка `:25` сохраняет все подтверждённые находки, а не список одного адверсария.

В beacon проверка тоже идёт только в CI. `docs/adrs/0022-ci-only-verification.md:7-11` запрещает локальные сборки и тесты, приёмка это зелёный CI в PR. `.opencode/skills/red-verdict-triage/SKILL.md:12-18` требует сверить `failedChecks` со своим diff и выбрать одну из трёх причин. Разрыв в том, что ревью кода само по себе не имеет правила: находка без команды и вывода не блокирует.

CP-05 предлагает добавить это правило в описание ревью и в `red-verdict-triage`. Комментарий ревью становится блокирующим только с воспроизводимой командой и наблюдаемым выводом. Судья из другого вендора остаётся опцией, не обязательной проверкой.

## 3. Карточки

| ID | Приём | Приоритет | Ссылка |
| --- | --- | --- | --- |
| CP-01 | Чистый классификатор вердикта на снимке | P2 | `docs/tasks/CP-01.md` |
| CP-02 | Один worker.md и первая строка kickoff'а | P1 | `docs/tasks/CP-02.md` |
| CP-03 | Предикат выхода и журнал на итерацию | P2 | `docs/tasks/CP-03.md` |
| CP-04 | Заморозка координат и доверенный маркер в ночной смене | P1 | `docs/tasks/CP-04.md` |
| CP-05 | Находка выживает только с доказанным прогоном | P1 | `docs/tasks/CP-05.md` |

Карточки не назначают OwnerFiles в коде. Владельцы файлов в них помечены как кандидаты и требуют отдельного решения до выдачи задачи.

## 4. Что не нашлось и почему это не вывод

`VL`, `game loop`, `deep module` и `grilling` буквально в клоне отсутствуют. Нельзя сослаться на несуществующий файл. Приёмы выше обсуждают реальные механизмы с теми же функциями: типизированный вердикт, внедрение контракта воркера, stop-hook цикл, доверенный маркер, ревью с судьёй из другого вендора.

Каталог `commands/` отсутствует во всех 159 плагинах. Заявленные slash-команды это `name` из frontmatter skills. Это важно для сравнения. Beacon хранит команды в CLI `cmd/beacon`, а cursor/plugins хранит их в markdown-фронтматтере.

Файл `research-pstack-skills.md` не читался. Пересечение с возможным соседним отчётом ограничено упоминаниями `arena` и `interrogate` как частей механизма, без пересказа каталога skills.

## 5. Источники

- `/tmp/opencode/cursor-plugins-research`, клон `https://github.com/cursor/plugins`, коммит `7a583913eb1b8d1a0ee76753d1a2cf072e7b54f9`, прочитано 2026-11-22.
- `pstack/.cursor-plugin/plugin.json`, `pstack/plugin.json`, `.cursor-plugin/marketplace.json`, `.claude-plugin/marketplace.json`.
- `pstack/docs/guide/04-design.md`, `pstack/docs/guide/07-overnight.md`, `pstack/docs/guide/08-principles.md`.
- `pstack/skills/poteto-mode/SKILL.md`, `worker.md`, `playbooks/autonomous-run.md`, `playbooks/babysit.md`, `playbooks/hillclimb.md`, `playbooks/autopilot-full.md`.
- `pstack/skills/poteto-mode/scripts/watch-pr/types.ts`, `policy.ts`, `render.ts`, `github.ts`, тесты и фейки.
- `pstack/agents/poteto-agent.md`, `pstack/agents/comment-sicko.md`.
- `pstack/automations/benny/FOR_AGENTS.md` и оба SKILL.md с references и templates.
- `advisor/hooks/`, `continual-learning/hooks/`, `ralph-loop/hooks/`.
- `orchestrate/skills/orchestrate/SKILL.md`, `create-plugin/rules/plugin-quality-gates.mdc`, `cursor-team-kit/rules/`.
- Beacon: `README.md`, `AGENTS.md`, `internal/domain/types.go`, `.opencode/skills/night-shift/`, `.opencode/skills/red-verdict-triage/`, `docs/adrs/0022-ci-only-verification.md`, по `origin/main` `2d34645b54f45d84dba3a595350f17f8658759da`.

# Сравнение с mattpocock/skills: приёмы, которых нет в beacon

Дата: 2026-11-22. Метод: полный клон https://github.com/mattpocock/skills в `/tmp/opencode/matt-skills-full` глубиной 1, коммит `abeeaf9` (Merge pull request #1455 from mattpocock/ci-frontmatter-check). Код beacon не менялся. `go test`, `go build`, `beacon gates` и `batch-apply` не запускались. Ссылки на beacon даны по `origin/main` `2d34645b`, проверено в worktree `worktrees/comp-matt-skills`.

Прочитаны все 39 файлов `SKILL.md`, 28 справочных файлов в каталоге `skills/` целиком и ещё 5 справочников `in-progress/software-factory` по разделам, скрипты из `scripts/`, шаблон `skills/engineering/wizard/template.sh` целиком (223 строки), а также `CLAUDE.md`, `README.md`, `GLOSSARY.md`, `SCOPE.md`, `CONTRIBUTING.md`, `.agents/*`, `.out-of-scope/*` и README всех пяти бакетов. `CHANGELOG.md`, `package-lock.json` и страницы `docs/engineering`, `docs/productivity` просмотрены выборочно и не служили источником выводов. Другой агент изучал тот же репозиторий через `webfetch`, здесь чтение шло по локальному клону.

## 1. Граница сравнения

Набор скиллов mattpocock уже стоит в `~/.config/opencode/skills` и доступен агенту как инструмент. Поэтому слово «отсутствует» означает здесь другое. Приём не зафиксирован в самом репозитории beacon: в `AGENTS.md`, `GLOSSARY.md`, `docs/templates`, `.opencode/skills`, ADR или коде. Репозиторий не проверяет его и не переносит на другую машину. Глобальная установка не является контрактом проекта.

Поиск отсутствия делался через `git grep` по явному списку каталогов `AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/adrs internal cmd tools scripts`. Каталог `docs/research` исключён: там уже лежат прошлые отчёты, которые упоминают чужие термины. Для каждого приёма в карточке записана команда и её результат.

## 2. Что прочитано

| Источник | Объём | Что дал |
| --- | --- | --- |
| `skills/engineering/` | 20 `SKILL.md` и справочники | TDD, диагностика, deep modules, domain modeling, review, PR, прототипы, оркестрация скиллов |
| `skills/productivity/` | 7 `SKILL.md` и справочники | grilling, handoff, teach, writing-for-agents, wait-what |
| `skills/in-progress/` | 8 `SKILL.md` и справочники | software factory, chief of staff, loop-me, deep modules в TypeScript, writing skills |
| `skills/misc/` | 4 `SKILL.md` и скрипт hook | git guardrails, pre-commit, shoehorn, scaffold |
| `scripts/`, `.agents/`, `.out-of-scope/` | 4 скрипта, 4 мета-файла, 9 записей | проверка frontmatter в CI, правила инвокации, writing docs, SCOPE, отклонённые идеи |
| `CLAUDE.md`, bucket README | 6 файлов | деление на promoted и non-promoted скиллы, обязательный router `ask-matt` |

Ключевые справочники, прочитанные целиком: `tdd/tests.md`, `tdd/mocking.md`, `diagnosing-bugs/scripts/hitl-loop.template.sh` (44 строки), `codebase-design/DEEPENING.md`, `codebase-design/DESIGN-IT-TWICE.md`, `domain-modeling/GLOSSARY-FORMAT.md`, `domain-modeling/ADR-FORMAT.md`, `code-review` smell baseline внутри `SKILL.md`, `improve-codebase-architecture/HTML-REPORT.md`, `prototype/LOGIC.md`, `prototype/UI.md`, `ask-matt/PHASE-BOUNDARIES.md`, `triage/AGENT-BRIEF.md`, `triage/OUT-OF-SCOPE.md`, `wizard/template.sh`, `pr/SKILL.md`, `writing-for-agents/SKILL-MECHANICS.md`, `teach/*-FORMAT.md`, `setup-matt-pocock-skills/*issue-tracker*.md`.

## 3. Пять приёмов

| Карточка | Приём | Источник в клоне | Состояние в beacon |
| --- | --- | --- | --- |
| CM-01 | Порог входа в диагностику: одна команда, уже запущенная и способная упасть на этом баге, до любой гипотезы | `skills/engineering/diagnosing-bugs/SKILL.md:18-66`, особенно `:57-66` | Есть изоляция Watch и логи в `AGENTS.md:37-44`, нет требования к воспроизводимой красной команде |
| CM-02 | Минимальный репро, 3-5 ранжированных опровержимых гипотез, теги `[DEBUG-...]` и контрольный замер для perf | `skills/engineering/diagnosing-bugs/SKILL.md:78-112`, `:130-138` | `internal/domain/depth.go:60-72` хранит одну гипотезу с falsifier на переходе, без ранжирования и точки показа пользователю |
| CM-03 | Проверка глубины модуля: deletion test, правило одного и двух адаптеров, глубина как выгода интерфейса, а не отношение строк | `skills/engineering/codebase-design/SKILL.md:62-65`, `:105-109`; `DEEPENING.md:27-37` | Есть слои в `AGENTS.md:88-93`, но ни одного из трёх тестов нет. `git grep` по `deletion test` даёт 0 |
| CM-04 | Вертикальные срезы tracer bullet и исключение для широкого рефактора expand-contract с зелёной интеграционной веткой | `skills/engineering/to-tickets/SKILL.md:25-40`; `tdd/SKILL.md:28-38` | Граф карточек и frontier есть, а правила срезов и expand-contract в репозитории нет. `git grep` по `tracer bullet` и `vertical slic` даёт 0 |
| CM-05 | Чистота глоссария и порог для ADR по трём условиям | `skills/engineering/domain-modeling/SKILL.md:60-74`; `GLOSSARY-FORMAT.md:25-30`; `ADR-FORMAT.md:29-37` | `GLOSSARY.md:3`, `:40`, `:48`, `:52` смешивают термины с путями кода, таймингами и ADR. Условий «hard to reverse / surprising / real trade-off» в репозитории нет |

Детали, команды проверки и шаги лежат в карточках `docs/tasks/CM-01.md` ... `docs/tasks/CM-05.md`.

## 4. Что уже есть, новые карточки не нужны

| Приём конкурента | Где уже в beacon | Почему карточку не создаём |
| --- | --- | --- |
| Опровержимая гипотеза с критерием опровержения | `internal/domain/depth.go:2-4`, `:60-72`, `:93-115` | Переход на слой глубже уже требует hypothesis, falsifier и новые evidence. Повтор на старых данных отвергается. CM-02 добавляет только ранжирование и внешний порог, саму гипотезу не дублируем |
| Progressive disclosure справочников | `docs/adrs/0035-o-claude-code-mechanics-features.md:13`, `.opencode/skills/night-shift/references/comment-rules.md:3-5` | ADR 0035 п3 предписывает короткий `SKILL.md` и чтение справочника перед действием |
| Явное управление авто-вызовом скилла | `docs/adrs/0035-o-claude-code-mechanics-features.md:12` | Там же п2: модель, человек или оба. Поле `disable-model-invocation` уже используется в `.opencode/skills/night-shift/SKILL.md` |
| Детерминированная проверка перед действием | `docs/adrs/0035-o-claude-code-mechanics-features.md:15` | Пункт 5 описывает запрет комментария без нового факта и проверку в `beacon-bridge.js` |
| Измерение до выбора подхода | `AGENTS.md:27-35` | Карточка или ADR без `baseline` проваливается. Поле `baseline` заполняется в карточках серии RS-CMP |
| Отказ от синонимов в домене | `GLOSSARY.md:9-11`, `:27-29` | Формат `_Avoid_` уже совпадает с `domain-modeling/GLOSSARY-FORMAT.md:25-30`. CM-05 трогает только примесь реализации внутри определений |
| Требование свежего SHA перед мержем | `docs/tasks/RS-CMP-01.md:2-10` | Отдельная карточка уже разбирает проверку против свежего `main` и ADR 0266 |

## 5. Другие приёмы, оставшиеся без карточек

| Приём | Источник | Причина |
| --- | --- | --- |
| Router скилл как карта потоков: main flow, on-ramps, vocabulary underneath | `skills/engineering/ask-matt/SKILL.md:13-79` | Большой отдельный приём. В beacon роль карты частично выполняют `AGENTS.md:54-79` и `README.md`. Нужна отдельная карточка после решения, нужен ли repo-уровневый router |
| Дерево решений на границе фазы с сравнением primary и secondary sources | `ask-matt/PHASE-BOUNDARIES.md:17-55` | Частично покрыто разделом «После сжатия» в `AGENTS.md:99-101` и инструкциями по worktree. Требует отдельного измерения частоты потерь контекста |
| Двухосевой review Standards и Spec параллельными сабагентами | `code-review/SKILL.md:6-12`, `:76-89` | Скилл установлен глобально. В репозитории нет ни контракта, ни проверки. Кандидат в следующий пакет |
| PR body как запись о риске: минимальная визуализация, before и after, one-way или two-way door, blast radius | `pr/SKILL.md:12-33`, `:158-170` | `AGENTS.md:137` требует только unslop для PR body, но шаблона нет. `blast radius` в активных артефактах даёт 0 совпадений |
| Wide refactor как expand-contract с батчами по blast radius | `to-tickets/SKILL.md:40` | Вошёл в CM-04 как вторая половина приёма |
| Brakes и throttle для автоматических триггеров, вопрос «что мешает двум запускам сделать одно и то же» | `in-progress/software-factory/SKILL.md:54-62`, `:86-96` | Пакет in-progress. У beacon есть идемпотентность и лимит fix attempts, но нет единого чек-листа проектирования фабрики |
| Leading words, no-ops и проверка «окружение как источник истины» | `writing-for-agents/SKILL.md:61-81` | Частично пересекается с `unslop` в `AGENTS.md:135-137`. Отдельный приём для будущей правки steering-файлов |
| Проверка правила на простом примере: сначала pass, потом ожидаемый fail, потом снова pass | `in-progress/setup-ts-deep-modules/SKILL.md:79-87` | У beacon есть культура базовых линий в карточках, но нет обязательного негативного контроля для правил линтера |
| Wiki с датированными утверждениями и supersede вместо переписывания истории | `in-progress/chief-of-staff/SKILL.md:26-48` | Относится к долгоживущим сессиям. Для beacon отдельный дизайн, не docs-правка |

## 6. Карточки

- `docs/tasks/CM-01.md`: ввести порог красной команды воспроизведения до гипотезы.
- `docs/tasks/CM-02.md`: ранжированные гипотезы, минимальный репро и теги отладочных логов.
- `docs/tasks/CM-03.md`: зафиксировать deletion test и правило адаптеров как критерии глубины.
- `docs/tasks/CM-04.md`: вертикальные срезы и expand-contract для широких рефакторов.
- `docs/tasks/CM-05.md`: отделить термины от реализации в `GLOSSARY.md` и ввести порог ADR.

## 7. Источники

- Локальный клон: `/tmp/opencode/matt-skills-full`, коммит `abeeaf9`, дата чтения 2026-11-22.
- Ссылки на конкретные файлы и строки даны в таблице раздела 3 и в карточках.
- Локальная база сравнения: `origin/main` `2d34645b`, worktree `worktrees/comp-matt-skills`.

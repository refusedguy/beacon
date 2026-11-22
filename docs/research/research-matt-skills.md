# Исследование: skills из mattpocock/skills против beacon

Дата: 2026-11-22. Режим только чтение кода и документации. `go test`, `go build` и `beacon gates` не запускались. Код не менялся.

Источник: https://github.com/mattpocock/skills, клон в `/tmp/opencode/matt-skills-research`, HEAD `abeeaf95660ba9e8b7c753eb38f441ca707a8823` от 2026-11-19. Сверка с beacon по `origin/main` на коммите `2d34645b54f45d84dba3a595350f17f8658759da`.

## Что проверялось

Прочитан `SKILL.md` каждого из 39 скиллов в каталоге `skills/`. Дополнительно проверены `.claude-plugin/plugin.json`, `skills-lock.json`, `README.md`, `AGENTS.md`, `GLOSSARY.md`, `docs/design/`, `docs/research/`, `docs/tasks/` и `.opencode/` на `origin/main`.

Скилл считается установленным, если его имя есть в списке `MATT_SKILLS` в `~/.config/opencode/skill-sources/sync_skills.py` и в массиве `skills` из `.claude-plugin/plugin.json`, а каталог скилла присутствует в `~/.config/opencode/skills`. Таких 27. Остальные 12 автором не включены в промоушен: 8 лежат в `skills/in-progress/`, 4 в `skills/misc/`.

Принцип недублирования. В `docs/tasks/HD-099.md`, `HD-109.md`, `HD-105.md`, `HD-106.md` и `HD-113.md` карточки на добавление скиллов закрыты как дубли: требование уже выполнено установленной библиотекой или существующим ADR. Поэтому для 27 установленных скиллов карточки не создаются. Для 12 остальных карточка создаётся только при конкретном отсутствующем артефакте в `origin/main`, который относится к работе beacon.

## Таблица всех 39 скиллов

| № | Скилл | Категория | Назначение | Ключевой приём | В библиотеке | Сверка с `origin/main` | Вердикт | Карточка |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | `ask-matt` | engineering | Маршрутизатор по скиллам и потокам | главный поток idea → ship, два on-ramp, пять вариантов на границе фазы | да | инвентарь зафиксирован в ADR 0253 и `docs/evals/hd53-skill-inventory.md`, HD-105 done | нет разрыва | нет |
| 2 | `codebase-design` | engineering | Словарь глубоких модулей | deletion test, интерфейс как поверхность теста, один адаптер даёт гипотетический seam | да | термины загружены глобально, HD-099 и HD-109 закрыты по этому основанию | нет разрыва | нет |
| 3 | `code-review` | engineering | Двухосевой разбор diff | параллельные сабагенты Standards и Spec, baseline из 12 запахов Fowler | да | `README.md:343-346` называет `code-review` аналогом `aif-review`, HD-106 done | нет разрыва | нет |
| 4 | `diagnosing-bugs` | engineering | Диагностика сложных багов | быстрый воспроизводимый цикл до гипотез, 3-5 опровержимых гипотез, теги для отладки | да | `README.md:346` называет скилл аналогом `aif-fix`, HD-106 done | нет разрыва | нет |
| 5 | `domain-modeling` | engineering | Построение глоссария и ADR | оспорить термин сразу, ADR только при трёх условиях | да | `GLOSSARY.md` в репо с такой же структурой и avoid-терминами | нет разрыва | нет |
| 6 | `grill-with-docs` | engineering | Интервью со следами в документах | вызывает `grilling` и `domain-modeling`, сохраняет решения в GLOSSARY и ADR | да | скилл в `~/.config/opencode/skills`, отдельной карточки нет | нет разрыва | нет |
| 7 | `implement` | engineering | Реализация тикета | `tdd` на согласованных seam, потом `code-review`, коммит в текущую ветку | да | `beacon-cycle` предписывает тот же цикл executor-агента | нет разрыва | нет |
| 8 | `implement-spec` | engineering | Реализация целого spec | task graph, frontier, свой worktree на тикет, integration branch, merger-сабагенты | да | `beacon-cycle` и `beacon-executor` задают worktree и merge loop | нет разрыва | нет |
| 9 | `improve-codebase-architecture` | engineering | Поиск возможностей углубить модули | HTML-отчёт с Mermaid и Tailwind, до и после, три уровня рекомендации | да | скилл установлен, отдельной карточки на HTML-отчёты в репо нет | нет разрыва | нет |
| 10 | `prototype` | engineering | Разовый прототип на вопрос дизайна | ветка logic или UI, состояние на каждом шаге, фиксация на prototype-ветке | да | скилл установлен, `beacon-cycle` ссылается на prototype | нет разрыва | нет |
| 11 | `pr` | engineering | Тело PR | Summary с диаграммой, Evidence до и после, Merge Danger one-way или two-way | да | `README.md:345` называет `pr` частью аналога `aif-review` | нет разрыва | нет |
| 12 | `research` | engineering | Исследование из первоисточников | фоновый агент с чистым контекстом, цитируемый Markdown в репо | да | `docs/research/` с 80+ файлами, профиль `issue-scout` в `.opencode/agents/` | нет разрыва | нет |
| 13 | `retro` | engineering | Разбор прошедшей сессии | предложения по окружению, механические ошибки в проверки, решения в стандарты | да | скилл установлен, карточек на retro в бэклоге нет | нет разрыва | нет |
| 14 | `setup-matt-pocock-skills` | engineering | Разовая настройка трекера, меток и доменных доков | explore, present, confirm, write, seed-шаблоны | да | скилл установлен, конфиг в репо лежит в `.opencode/` | нет разрыва | нет |
| 15 | `tdd` | engineering | Красный и зелёный цикл | только на согласованных seam, вертикальные срезы, рефакторинг вне цикла | да | `HD-109` закрыт как дубль `tdd` и `tdd-bugfix` | нет разрыва | нет |
| 16 | `to-spec` | engineering | Спека из диалога | длинный список user stories, решения о тестировании и seam без путей к файлам | да | скилл установлен, карточки spec-потока в бэклоге есть | нет разрыва | нет |
| 17 | `to-tickets` | engineering | Разбиение на тикеты | tracer-bullet срезы, blocking edges, expand-contract для широких рефакторов | да | граф рёбер `blocks`, `depends-on`, `supersedes` и `related` описан в `AGENTS.md` | нет разрыва | нет |
| 18 | `triage` | engineering | State machine триажа | проверка утверждения до grilling, agent brief, wontfix по трём причинам | да | `night-shift-manager` в `.opencode/skills/`, карточки HD на triage есть | нет разрыва | нет |
| 19 | `wayfinder` | engineering | Карта решений для большого неясного объёма | decision tickets, fog of war, одно решение за сессию, research на throwaway-ветках | да | скилл установлен, research-ветки и контекстные указатели приняты в cycle | нет разрыва | нет |
| 20 | `wizard` | engineering | Bash-визард для шагов человека | библиотека над `STAGES`, скрытый ввод секретов, идемпотентные записи `.env` | да | скилл установлен, human-only шаги описаны в документах | нет разрыва | нет |
| 21 | `grilling` | productivity | Допрос до общего понимания | design tree, rounds, frontier, агент ищет факты сам | да | скилл загружен в `AGENTS.md` по триггеру stress-test | нет разрыва | нет |
| 22 | `grill-me` | productivity | Stateless-версия grilling | вызывает `grilling`, не пишет в репо | да | скилл установлен | нет разрыва | нет |
| 23 | `handoff` | productivity | Передача контекста свежему агенту | документ во временный каталог ОС, секция suggested skills | да | скилл установлен, beacon-cycle использует указатели вместо дублирования | нет разрыва | нет |
| 24 | `teach` | productivity | Обучение по сессиям | MISSION, learning records, ZPD, переиспользуемые компоненты уроков | да | скилл установлен | нет разрыва | нет |
| 25 | `to-questionnaire` | productivity | Опросник для человека с нужными знаниями | допрос о отправке, вопросы на разрыв знаний | да | скилл установлен | нет разрыва | нет |
| 26 | `wait-what` | productivity | Переформулировка непонятого сообщения | Simplified Technical English плюс словарь из GLOSSARY | да | скилл установлен | нет разрыва | нет |
| 27 | `writing-for-agents` | productivity | Документы для агентов | context pointers, information hierarchy, leading words, completion criteria | да | скилл загружен глобально, `AGENTS.md` в beacon использует те же указатели | нет разрыва | нет |
| 28 | `chief-of-staff` | in-progress | Долгосрочная цель через сабагентов | wiki с датированными утверждениями, ingest и query и lint, retro на каждом пятом сабагенте | нет | ADR и `docs/research/` уже датированы и ссылаются на коммит, индекс research-файлов отсутствует; карточку не создаём, потому что разрыв покрыт ADR и research-файлами | нет измеримого разрыва | нет |
| 29 | `claude-handoff` | in-progress | Передача в фонового `claude` агента | `claude --bg --name` с файлом промпта | нет | beacon работает через OpenCode и свой wake registry, `handoff` установлен | не применимо | нет |
| 30 | `loop-me` | in-progress | Спецификация личных циклов | trigger, checkpoint, push right, brief | нет | тема личных рабочих циклов в `origin/main` отсутствует и не относится к наблюдению за PR | не применимо | нет |
| 31 | `setup-ts-deep-modules` | in-progress | Глубокие модули в TypeScript | dependency-cruiser, entry points вместо barrel, проверка что правила кусают | нет | beacon на Go, `go.mod`, слои описаны в `AGENTS.md`, TypeScript-конфигов нет | не применимо | нет |
| 32 | `software-factory` | in-progress | Словарь проектирования фабрики ПО | Task, Trigger, Run, Runner; services и tasks; пять вопросов на задачу; три тормоза | нет | слов `software factory`, `ticket source` и `observability platform` в `README.md`, `AGENTS.md` и `GLOSSARY.md` нет, ответов на пять вопросов в docs нет | разрыв | MSK-01 |
| 33 | `writing-beats` | in-progress | Сборка статьи из битов | grounding понятий, выбор starting beat, по одному биту за шаг | нет | тема написания статей в `origin/main` отсутствует | не применимо | нет |
| 34 | `writing-fragments` | in-progress | Добыча черновых фрагментов | leading word, фрагменты через горизонтальную линию | нет | тема написания статей в `origin/main` отсутствует | не применимо | нет |
| 35 | `writing-shape` | in-progress | Придание формы сырому материалу | абзац за абзацем, спор о формате блока | нет | тема написания статей в `origin/main` отсутствует | не применимо | нет |
| 36 | `git-guardrails-claude-code` | misc | Хук против опасных git-команд | блок `git push`, `reset --hard`, `clean -f`, `branch -D` | нет | `AGENTS.md` описывает свой хук `execute.before` против `grep -r`, `find /` и локальных go test | покрыто своим механизмом | нет |
| 37 | `migrate-to-shoehorn` | misc | Миграция `as` в тестах TypeScript | `fromPartial`, `fromAny`, `fromExact` | нет | beacon на Go, TypeScript-тестов нет | не применимо | нет |
| 38 | `scaffold-exercises` | misc | Каркас учебных упражнений | секции `XX`, упражнения `XX.YY`, lint | нет | учебных курсов в репо нет | не применимо | нет |
| 39 | `setup-pre-commit` | misc | Husky, lint-staged, Prettier | `husky init`, `.lintstagedrc`, smoke-коммит | нет | в репо есть `.githooks/` и pre-commit на индекс, JS-инструментов нет | покрыто своим механизмом | нет |

Итого: 39 скиллов, 27 установлены, 12 не включены в промоушен. Карточек 1.

## Разрыв для MSK-01

Исходный `skills/in-progress/software-factory/SKILL.md` даёт словарь для системы агентов, которая превращает тикеты, PR и расписания в слитый код. Beacon именно такая система. В `origin/main` её описывают `README.md`, `AGENTS.md`, `GLOSSARY.md` и `docs/design/`, но на уровне компонентов. Уровня описания задач и сервисов, который даёт skill, там нет.

Измерение до выбора, выполнено чтением `origin/main`:

- `git grep -i -c -E 'software.?factory|ticket source|observability platform' origin/main -- README.md AGENTS.md GLOSSARY.md` даёт 0 совпадений.
- `git grep -i -c -E 'what triggers it|what does it trigger|where does a human re-enter|what stops two runs' origin/main` даёт 0 совпадений.
- `git ls-tree -r --name-only origin/main -- docs/design` даёт 3 файла: `agent-registry-schema.md`, `mcp-sdk-choice.md`, `opencode-cli-manager.md`. Ни один не разбирает beacon как фабрику.

Что уже закрывает соседние потребности, чтобы не дублировать:

- `GLOSSARY.md` задаёт Roles, Watch, Subscriber, Verdict, Obligation и ReconcileLoop. MSK-01 не переписывает этот словарь, а добавляет уровень задач и сервисов.
- `AGENTS.md` задаёт границу инструмента, claim, граф зависимостей и вычисление вердикта. Пять вопросов из skill применяются к задачам, а не к правилам агента.
- `maxFixAttempts = 3` в `GLOSSARY.md` уже ограничивает цикл FIX-агентов. Тормоза из skill не заменяют этот лимит, а объясняют, какие проверки нужны до нового триггера.
- HD-105 и HD-106 закрыли инвентарь и таблицу аналогов скиллов. MSK-01 не повторяет эти таблицы.

Карточка MSK-01 лежит в `docs/tasks/MSK-01.md`. Она меняет только `docs/design/` и не трогает код, ADR и существующие карточки.

## Дополнительная находка без карточки

Глобальная библиотека отстаёт от upstream HEAD в трёх файлах: `codebase-design/DESIGN-IT-TWICE.md`, `setup-matt-pocock-skills/issue-tracker-github.md` и `setup-matt-pocock-skills/issue-tracker-gitlab.md`. Файлы отличаются по байтам при сверке каталогов. Карточки нет, потому что `sync_skills.py` лежит вне репозитория, и HD-113 уже закрыл подобную карточку по этой причине. Находка фиксируется здесь для владельца окружения.

## Итог

Разрыв один, и он в документации проектирования. Остальные 38 скиллов либо уже установлены и загружены в агента, либо не применимы к Go-сервису наблюдения за PR, либо покрыты собственными механизмами beacon. Карточки на установленные скиллы не создавались, чтобы не повторить HD-099, HD-105, HD-106 и HD-109.

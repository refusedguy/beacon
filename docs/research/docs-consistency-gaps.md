# Консистентность ADR, глоссария и docs-индекса

Отчёт по состоянию `origin/main` на коммите `a7dcb6f6e695fddb3ff5092c54ade9a69863d0bb`. Проверены `docs/adrs/`, `GLOSSARY.md`, `docs/ROADMAP_STATUS.md`, `docs/runbooks/`, `README.md`, `docs/tasks/` и генераторы в `internal/infra/batchapply/apply.go` и `tools/roadmap-sync/main.go`.

## Сверка с PR 1264 и 1261

PR 1264 (`bf4a08ec`, BCX-02) убрал дубли номеров ADR. PR 1261 (`e5f3a22c`, BCX-06) переименовал 66 файлов `NNNN-untitled.md` в файлы с темами. Оба коммита лежат в `origin/main`.

Замер по текущему дереву: 305 файлов ADR, 305 уникальных номеров, диапазон 0001-0305 без пропусков. Повторов номеров нет. Заголовок и Статус есть в каждом файле. Ссылок вида `ADR NNNN` на несуществующий номер нет. Часть обещаний из карточек BCX-02, BCX-05, BCX-06 и T3.449 устарела: в них указаны 24 или 34 дубля и 299 или 302 файла. Эти карточки в отчёте не менялись.

Переименование и перенумерация оставили 78 ссылок на старые имена файлов. Номер в такой ссылке существует, а путь уже нет.

## Запрошенные документы

Три пути из задания отсутствуют в дереве `origin/main`.

| Путь | Состояние | Где живёт сейчас |
| --- | --- | --- |
| `docs/adrs/README.md` | Нет в git | Производный индекс, убран PR 905 (IDX-11), путь в `.gitignore:26` |
| `docs/GLOSSARY.md` | Нет в git | Актуальный словарь лежит в `GLOSSARY.md` в корне |
| `docs/FACTS.md` | Нет в git | Производный документ, убран PR 905, путь в `.gitignore:24` |
| `docs/ROADMAP_STATUS.md` | Есть в git | Содержимое устарело, см. ATX-D06 |

PR 929 и 1169 убрали запись производных файлов из генераторов. PR 1169 добавил сборку индекса в `tools/roadmap-sync`. Индекс теперь создаётся по запросу, а не лежит файлом в репозитории. Карточки и роадмап продолжают ссылаться на старые пути.

## Таблица находок

| ID | Находка | Замер и доказательство | Proposal | Приоритет | OwnerFiles |
| --- | --- | --- | --- | --- | --- |
| ATX-D01 | Дубли номеров ADR устранены, но регрессия не закреплена тестом на текущем дереве | 305 файлов, 305 номеров, 0 дублей. `docs-baseline.json` содержит `baseline: []` и `baseline_limit: 40`. Gate уже умеет `DOC-ADR-DUP`, но в базовой строке нет nursery-записи | Добавить в docsgate self-test или отдельный тест, который считает дубли по текущему дереву и требует 0. Держать baseline пустым | P1 | `tools/beacon-gates/docsgate/docs.go`, `tools/beacon-gates/gate/config/docs-baseline.json` |
| ATX-D02 | Ссылки на несуществующие пути ADR после PR 1261 и 1264 | 78 ссылок в 21 файле. Примеры: 35 в `CLEAN-05.md`, 15 в `docs/adrs/0067-omnigraph-wave-aw.md`, 3 в `docs/adrs/0269-mcp-ingress-control-plane.md`, 3 в `T3.361.md`, 2 в `BCX-06.md`, 2 в `BCX-02.md`. `CheckLinks` проверяет только inline links, не backticks | Добавить в gate проверку путей ADR внутри backticks. Исправить ссылки в ADR-файлах. Карточки отдать их владельцам, в этом PR их не трогать | P1 | `tools/beacon-gates/gate/gate.go`, `docs/adrs/0067-omnigraph-wave-aw.md`, `docs/adrs/0269-mcp-ingress-control-plane.md` |
| ATX-D03 | Источники research, на которые ссылаются карточки, отсутствуют в git | 19 filenames, 133 ссылки. Лидеры: `error-codes-audit.md` 12, `mcp-contract-drift-audit.md` 11, `cli-ux-audit.md` 10, `docs-health-audit.md` 10, `adr-consistency-audit.md` 9. Часть файлов лежит в каталоге handoff как untracked | Выбрать одно: закоммитить источники или заменить ссылки на существующий отчёт. Добавить проверку существования `docs/research/*.md`, на который ссылается карточка | P1 | `tools/beacon-gates/docsgate/docs.go`, `docs/research/docs-consistency-gaps.md` |
| ATX-D04 | Три запрошенных docs-пути отсутствуют, а канонического указателя нет | `docs/adrs/README.md`, `docs/GLOSSARY.md`, `docs/FACTS.md` отсутствуют. PR 905, 929 и 1169 перевели их в разряд производных или корневых. `.gitignore:24-26` хранит три пути | Описать в `README.md` статус каждого документа и команду получения. Ссылки в карточках оставить владельцам, новый указатель вести в этом отчёте | P2 | `README.md`, `docs/research/docs-consistency-gaps.md` |
| ATX-D05 | Термины без определения и расхождение списка вердиктов | В `GLOSSARY.md` 23 термина. `green_blocked` описан в статье Verdict, но в `README.md:153` списка вердиктов его нет. `autoMerge`, `requireApproval`, `waitForMain` и `WIRING` не определены ни в глоссарии, ни в README | Добавить отдельные статьи для четырёх терминов. Свери список вердиктов README со списком из `internal/domain/types.go` и `GLOSSARY.md` | P2 | `GLOSSARY.md`, `README.md` |
| ATX-D06 | Роадмап оторвался от дерева | Baseline SHA `a67afaf`, 44 коммита позади `HEAD`. Заголовок обещает 4 реальных todo, таблица реестра содержит 206 строк. В OwnerFiles есть `docs/FACTS.md`, `docs/AGENT-RULES.md`, `docs/SKILL-MAP.md`, `docs/adrs/0189-internal-scheduler.md`, `docs/adrs/0190-mcp.md`, `docs/adrs/0198-outbox.md` | Пересчитать роадмап по текущему SHA. Добавить проверку, что число в заголовке равно числу строк реестра и что OwnerFiles существуют | P2 | `docs/ROADMAP_STATUS.md`, `tools/roadmap-sync/main.go` |
| ATX-D07 | Runbook ссылается на отсутствующий research-файл | `docs/runbooks/BLOCKER-CLOSE-PLAN.md:3` и `:114` ссылаются на `docs/research/task-lifecycle-brainstorm.md`. Файла нет в git. На него же ссылается `docs/adrs/0270-task-close-tool.md` | Заменить ссылку на существующий источник или убрать её. Распространить проверку путей на backticked `docs/...` в runbook | P2 | `docs/runbooks/BLOCKER-CLOSE-PLAN.md`, `docs/adrs/0270-task-close-tool.md`, `tools/beacon-gates/gate/gate.go` |
| ATX-D08 | Premise карточек про FACTS устарела | `docs/FACTS.md` не пишется генератором после PR 929. Функция `factsLink` в `apply.go` уже добавляет `../` для владельцев вне `docs/`. Карточка BCX-08 всё ещё требует этой правки и висит в todo | Подтвердить закрытие BCX-08 и HD-088 владельцем карточек. В генераторе оставить self-test разрешения ссылок. Запрет на ручное закрытие чужих карточек соблюдён | P3 | `internal/infra/batchapply/apply.go`, `docs/research/docs-consistency-gaps.md` |

## Как воспроизвести замеры

Команды выполняются из корня репозитория без сборки и тестов. Замеры 78 и 133 не включают новые карточки ATX-D и этот отчёт: они перечисляют находки как примеры и иначе завышают счёт.

```sh
git ls-tree -r --name-only HEAD -- docs/adrs/ | rg -c '\.md$'
git ls-tree -r --name-only HEAD -- docs/adrs/ | sed -E 's|.*/([0-9]{4})-.*|\1|' | sort | uniq -d
rg -n 'docs/research/[A-Za-z0-9_.-]+\.md' docs/tasks docs/adrs docs/runbooks docs/research
rg -n 'task-lifecycle-brainstorm' docs/runbooks docs/adrs
```

Gate `docs` в CI объявлен с `continue-on-error: true`. Его находки информационные и не блокируют merge.

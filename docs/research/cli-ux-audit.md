# Аудит UX CLI beacon

Дата: 2026-11-21. Репозиторий: beacon-impl, коммит `d3b1bfb1`. Область: `cmd/beacon/*.go`, `internal/app/taskcli`, `internal/infra/taskstore`, `internal/infra/observe/query.go`, `README.md`, `docs/adrs`, `docs/tasks`, `docs/design`. Worktree не читались. Код не менялся.

Метод: чтение dispatch и всех раннеров, затем сверка с каталогом, `usageText`, README и карточками. Для проверки фактического поведения собран один бинарник `./cmd/beacon` в `/tmp/opencode/beacon-audit`. Это не `go build ./...`, в репозитории файлы не менялись. Все выводы ниже подтверждены запуском этого бинарника с пустыми `BEACON_STATE_DIR` и `XDG_STATE_HOME`. Где вывод зависит от состояния, это отмечено.

## 1. Инвентарь: dispatch, каталог, usage

Три разных списка команд не совпадают. `usageText` в `cmd/beacon/main.go:20-53` описывает 28 строк (27 команд, `orchestrator` имеет две подкоманды). `dispatch` в `cmd/beacon/main.go:229-293` содержит 27 реальных подкоманд. `catalog` в `cmd/beacon/catalog.go:20-36` плюс три `init()` в `main.go:64-67`, `up.go:24-31` и `human.go:19` дают 18 записей.

| Источник | Команды | Отличие |
| --- | --- | --- |
| `dispatch` (`main.go:229-293`) | list, what, doctor, build, test, gates, task, why, mcp, orchestrate, orchestrator, agents, watch, spawn, attach, registry, deliver, ci, daemon, deferred, workload, metrics, pr, logs, inspect, human, reset | Рабочий набор. `registry` только `stop`, `ci` только `check`, `pr` только `create` |
| `usageText` (`main.go:20-53`) | Те же 27 плюс вторая подкоманда `orchestrator rebind` | Не описывает `up` и `gc` |
| `catalog` (`catalog.go` + 3 `init()`) | agents, attach, build, deferred, doctor, gates, human, inspect, list, logs, mcp, reset, spawn, task, up, watch, what, why | Нет 10 рабочих команд. Есть `up`, которую dispatch не знает |

Отсутствуют в каталоге: `test`, `orchestrate`, `orchestrator`, `registry`, `deliver`, `ci`, `daemon`, `workload`, `metrics`, `pr`. Проверено: `beacon what metrics` даёт `beacon what: неизвестная команда "metrics"` и код 2, хотя `beacon metrics` работает. `beacon what up` даёт код 0 и описание команды, которую нельзя запустить: `beacon up` отвечает `beacon: неизвестная подкоманда "up"`, код 2.

Это прямое нарушение приёмки `docs/tasks/T0.6-beacon-cli-binary.md:26`: «каталог в JSON полный и совпадает с usage». README строкой 187 обещает «полный каталог» через `beacon list --format json`.

### Команды с кодом, но без dispatch

`beacon registry list`, `beacon registry check`, `beacon registry sync` не запускаются. `runRegistry` в `registry_stop.go:214-228` знает только `case "stop"`. Раннеры и форматтеры есть: `registryListRunner` (`registry_list.go:50`, `--format table|json` на `:112`), `registryCheckRunner` (`registry_check.go:232`), `syncRegistry` (`registry_sync.go:174`). Проверено: `beacon registry list` даёт `beacon registry: неизвестная подкоманда "list"`, код 2.

`beacon gc` тоже не запускается. `gcRunner` и `runGC` с `--format table|json` и `--apply` живут в `watch.go:752-758` и `:821-872`. В `dispatch` нет `case "gc"`. Проверено: `beacon: неизвестная подкоманда "gc"`, код 2.

Карточки T5.53, T5.55 и T5.61 уже фиксируют проблему registry как известную и объясняют её границами OwnerFiles. Для `gc` такого описания нет.

### Сводка флагов по командам

| Команда | Флаги в коде | Формат вывода | Где описано |
| --- | --- | --- | --- |
| `list` | `--format` table\|json (`catalog.go:55`) | table, json | usage, catalog |
| `what` | нет | `name/summary/usage` | usage, catalog |
| `doctor` | `--watch`, `--interval`, `--session`, `--max-ticks`, `-- <cmd>` (`doctor.go:30-34,64-71`) | `name: STATUS detail` | usage и catalog только `beacon doctor` |
| `build`, `test` | нет | stderr потомка | usage |
| `gates` | аргументы потомка; скрытая подкоманда `rules` с `--root` (`commands.go:49,85-87`) | зависит от гейта | usage, catalog |
| `task` | 11 подкоманд; `--status`, `--phase`, `--agent`, `--file`, `--format`, `--owner-files`, `--dry-run`, другие (`task.go:154-214`, `taskcli/service.go:64-70`) | table, json | usage и catalog урезаны до `<list\|new\|take>` |
| `why` | нет | текст цепочки | usage, catalog |
| `agents` | `--format`, плюс скрытые `--route`, `--handoff`, `--to`, `--task`, `--repo`, `--worktree` (`agents.go:169-177`) | table, json; `--route`, `--handoff` и привязка всегда json | usage и catalog только `--format` |
| `watch` | `--interval`, `--live`, `--format`, скрытый `--wake` (`watch.go:217-221`) | table, json | usage без `--wake`; catalog без `--live` и `--wake` |
| `spawn` | `--agent`, `--title`, скрытые `--runtime acp`, `--runner` (`spawn.go:140-142`) | текст | usage, catalog |
| `attach` | скрытый `--resume [taskID]` (`attach.go:218-231`) | текст | usage и catalog только `<sessionID>` |
| `registry stop` | `--watch`, `--revision`, `--session`, `--token` (`registry_stop.go:236-240`) | `stop_allowed`/`stop_refused` | usage |
| `deliver` | нет; конфиг через `BEACON_DELIVER_*` (`deliver.go:317-321`) | `watch=... verdict=...` | usage |
| `ci check` | `--all`, `--dry-run`, `--repo`, `--format` (`checkall.go:29-34`) | table, json | usage |
| `daemon` | `--interval` 0 = адаптивный 10s/30s, `--once` (`daemon.go:176-178`) | stderr диагностика | usage; README другое |
| `deferred` | `--format` (`deferred.go:128`) | table, json | usage, catalog |
| `workload` | `--format` (`workload.go:125`) | table, json | usage, README |
| `metrics` | `--format` json\|prometheus (`observe.go:123`) | json, prometheus | usage |
| `pr create` | `--title`, `--body`, `--card`, `--base`, `--target`, `--adapter`, `--provider`, `--draft`, `--dry-run`, `--format` (`prcreate.go:285-295`) | table, json | usage урезан |
| `logs` | `--tail`, `--watch`, `--pr`, `--errors`, `--json` (`logs.go:23-27`) | текст или сырые строки | usage, catalog |
| `inspect` | `--json` (`inspect.go:105`) | table, json | usage, catalog |
| `human` | `--note` (`human.go:199`) | `human: verb target: ok` | usage, catalog |
| `reset` | `--yes` (`inspect.go:223`) | текст | usage, catalog |
| `orchestrate` | `--dry-run`, `--format` (`orchestrate.go:225-226`) | table, json | usage |
| `orchestrator bind/rebind` | `--session`, `--from`, `--to`, `--dry-run` (`orchestrator.go:92-93,234-236`) | текст | usage |

Глобальные скрытые флаги `--otel-exporter`, `--otel-endpoint`, `--otel-protocol` читаются в `setupObserve` и срезаются только для `daemon`, `orchestrator`, `ci` и `deliver` (`observe.go:19-60`). Для остальных команд они попадают в собственный `FlagSet` и дают `flag provided but not defined`. В usage их нет.

## 2. Консистентность отдельных флагов

### `--format`

Значения различаются по командам.

- `--format table|json`: `list`, `agents`, `deferred`, `workload`, `watch`, `gc`, `ci check`, `pr create`, `orchestrate`, `registry list`, `task list`.
- `--format json|prometheus`: только `metrics` (`observe.go:123`).
- `--json` вместо `--format`: `logs` (`logs.go:27`) и `inspect` (`inspect.go:105`).

Текст подсказки тоже различается: `catalog.go:55` и `agents.go:170` пишут просто «формат вывода», остальные «формат вывода: table или json». Поведение `--json` разное. В `inspect` это переключатель рендера, в `logs` это просьба не переформатировать строку из файла (`observe/query.go:83-87`). Проверено: `logs --format json` даёт `flag provided but not defined: -format`, код 2, после чего печатается общий usage. То же для `inspect --format json`.

`logs --json` не гарантирует JSON на выходе. Флаг сохраняет строку файла как есть. Если процесс писал logfmt, на выходе будет `time=... level=...`. Проверено с двумя разными `BEACON_LOG_FORMAT`: с `json` получилась JSON-строка, без него строка logfmt в том же выводе.

### `--project`

Реального флага нет ни в одной команде. Есть только комментарий в `registry_config.go:32`: «An explicit project root (the --project flag, passed in project)» и параметр `findRegistryConfig(project string)` (`registry_config.go:38`). Все вызывающие передают пустую строку или путь из теста.

Документация предписывает флаг: `docs/design/agent-registry-schema.md:31`, `docs/tasks/T5.14.md:12`, `docs/tasks/T5.28.md:14`, `docs/adrs/0086-project-scoped-agent-registry.md:17`. Отдельно `docs/adrs/0156-orchestrator-workload.md:21` и `docs/tasks/T3.383.md:12` описывают `beacon workload [--project <key>|--all]`. В `workload.go:123-133` есть только `--format`.

### `--status`

Только `beacon task list --status` (`task.go:155-157`). Допустимые значения `todo`, `in_progress`, `done`, `dropped` проверяются в `taskcli/service.go:80-84`. Ни одна команда просмотра вотч не имеет фильтра по статусу или вердикту.

### `--owner`

Флага нет. Ближайшие варианты: `beacon task new --owner-files` (`task.go:179`, спецификация `docs/beacon-task-cli-spec.md:121`) и `beacon task list --file`, который ищет пересечение путей с владельцами (`taskcli/service.go:99-116`). Фильтра по владельцу-агенту нет.

### `--once`

Есть в `daemon.go:178` и `up.go:143`. Команда `up` не подключена к dispatch. `daemon --once` всегда возвращает 0 (`daemon.go:238-244`), потому что `ReconcilePass` не возвращает ошибку. Сбой прохода не меняет код выхода.

### Usage и help

`parseFlags` (`catalog.go:38-51`) ставит `fs.Usage = func(){}` и при любой ошибке `Parse`, включая `--help`, печатает общий `usageText` в stderr и возвращает false. Раннеры возвращают 2. Проверено: `beacon watch --help`, `beacon list --help`, `beacon metrics --help`, `beacon doctor --help` дают общий usage в stderr и код 2. `beacon --help` даёт тот же usage в stdout и код 0 (`main.go:231-233`).

`task` и `spawn` используют `parseTaskArgs` (`task.go:113-147`), где `--help` это просто неизвестный флаг. Проверено: `beacon task --help` даёт `неизвестная подкоманда task "--help"` плюс `taskUsage`, код 1. Справки по конкретной команде нет нигде, хотя README строкой 187 прямо рекомендует `beacon what <command>` как такую справку. `beacon what` описывает только 18 команд из каталога.

## 3. Ошибки, вывод и коды выхода

### Неинформативные и неоднозначные ошибки

- Неизвестный флаг даёт сырое сообщение пакета `flag`: `flag provided but not defined: -format` без имени команды и без подсказки, что делать.
- `beacon registry stop` без флагов печатает `beacon registry stop: registry stop: expected watchID is required`. Двойной префикс и английский текст внутри русской команды. Внутренние ошибки в `registry_stop.go:84-90` уже содержат `registry stop:`, а вызывающий добавляет свой префикс в `:252`.
- `registry list` при неверном формате печатает английское `registry list: unknown format "yaml", expected table or json` (`registry_list.go:105`) без префикса `beacon`.
- `task list --format yaml` даёт `неизвестный формат "yaml"` (`taskcli/service.go:78`) без имени команды и без списка допустимых значений. Аналогичная ошибка в `list` (`catalog.go:76`) содержит и команду, и ожидаемые значения.
- `beacon pr create` вне git-репозитория печатает многострочный raw-вывод `git remote get-url` с упоминанием `GIT_DISCOVERY_ACROSS_FILESYSTEM`. Проверено в `/tmp`, где нет `.git`.

### Вывод, который нельзя разобрать

У части команд есть только строковый формат, хотя результаты имеют JSON-теги.

- `deliverResult` (`deliver.go:298-306`) объявлен с JSON-тегами, но `runDeliver` печатает `res.line()` (`deliver.go:308-311,465`), то есть `watch=... verdict=... delivery=... attempts=...`. Ни `--format json`, ни кодирования в JSON нет.
- `beacon human` печатает `human: approve ses_x: ok` (`human.go:263-272`).
- `beacon registry stop` печатает `stop_allowed watch=... revision=... session=...` (`registry_stop.go:268-272`).
- `beacon inspect` без `--json` печатает `WATCH id=... pr=... repo=... status=...`, `BLOCKER id=...` (`inspect.go:187-217`).
- `beacon doctor` печатает `go: OK go1.27.4` (`doctor.go:47-54`), формат не описан.
- `beacon workload` печатает `ACTIVE: 5`, `READY: 2` и строки `VERDICT ready: 2` (`workload.go:160-169`).
- `beacon metrics --format json` отдаёт JSON, `--format prometheus` отдаёт текст. Это единственная команда со вторым машинным форматом.

`inspect --json` на пустом состоянии даёт `{"watches":[],"blockers":null,"escalations":[]}`. Поле `blockers` это `null`, а не `[]`. Другие команды (`agents.go:334-336`, `registry_list.go:93-95`, `deferred.go` через `mcp.RenderDeferred`) намеренно превращают nil в пустой массив. Для потребителя JSON это лишняя ветка.

### stdout и stderr

Общее правило ADR 0016 зафиксировано в `docs/adrs/0016-build-system.md:24`: машинный поток в stdout, шум в stderr. Большинство команд ему следует. Исключения ниже.

- `orchestrate` (`orchestrate.go:245-258`), `workload` (`workload.go:142-156`) и `pr create` (`prcreate.go:318-341`) при ошибке пишут причину в stderr, но всё равно печатают результат в stdout и возвращают 2. Потребитель видит валидный JSON или таблицу при ненулевом коде. Это хуже, чем пустой stdout: результат можно принять за успех.
- `watch --wake --format json` направляет строки wake в stderr (`watch.go:258-261`), чтобы stdout остался чистым JSON. Это правильный пример.
- Общий usage при ошибке уходит в stderr (`catalog.go:42,47`), при `--help` верхнего уровня в stdout (`main.go:232`). Для справки это необычно.
- Запуск потомков через `emit` (`main.go:324-331`) сохраняет разделение потоков `build`, `test`, `gates`.

### Коды выхода

| Код | Где | Смысл |
| --- | --- | --- |
| 0 | все команды | успех |
| 1 | `task` и `spawn` через `taskstore.ExitValidation` (`taskstore/store.go:25`), `doctor` при FAIL (`doctor.go:55-56`), `deliver` при недоставке (`deliver.go:450,468`), `registry stop` при отказе (`registry_stop.go:270`) | найдено нарушение или результат не достигнут |
| 2 | `dispatch` и почти все раннеры | usage, неизвестная команда, ошибка запуска |

`taskstore` следует ADR 0016 буквально: 1 это валидация, 2 это ошибка запуска (`taskstore/store.go:22-27`). Остальной CLI использует 2 и для валидации. Один и тот же пользовательский провал, неверное значение `--format`, даёт 1 в `task list` и 2 в `list`. Проверено оба.

`daemon --once` возвращает 0 даже при неудачном проходе. `registry stop` при отказе возвращает 1 и печатает вердикт в stdout. Это осознанный контракт, но он не описан в usage.

## 4. Расхождения с документами

- `README.md:202` пишет про `beacon daemon`: «Шаг по умолчанию раз в три минуты». В коде `--interval` по умолчанию 0, что означает адаптивные 10s/30s (`daemon.go:177`, `github/tier1.go:25-28`). `usageText:42` пишет `--interval 10s|30s`, комментарий в `daemon.go:172` пишет `--interval 3m`. Три разных значения.
- `README.md:187` называет `beacon list --format json` полным каталогом. В нём нет 10 рабочих команд и есть неработающая `up`.
- `README.md:219` говорит, что `beacon human` «на main ещё не работает». Команда подключена в `main.go:284-285`, есть в каталоге, отвечает на вызов. Проверено: `beacon human` даёт осмысленную ошибку об отсутствии аргументов, код 2.
- `README.md:219` перечисляет глаголы `approve`, `context`, `rework`. В `humanVerbs` (`human.go:30-38`) нет `context`, но есть `reject`, `unblock`, `pause`, `resume`, `cancel`. usage и каталог перечисляют семь реальных глаголов.
- `README.md:194` и каталог описывают `task <list|new|take>`, а в `task.go:41-67` одиннадцать подкоманд.
- `docs/adrs/0156-orchestrator-workload.md:21` и `T3.383.md:12` обещают `workload --project|--all`. Реализация имеет только `--format`.
- `docs/design/agent-registry-schema.md:31`, `T5.14.md:12`, `T5.28.md:14`, `ADR 0086:17` обсуждают `--project` как существующий вход. Флага нет.
- `docs/tasks/T5.53.md`, `T5.55.md`, `T5.61.md` честно пишут, что registry-команды не подключены. README при этом не упоминает их вообще.
- `usageText:39` помечает `orchestrator rebind` как устаревший, README строкой 197 повторяет. Здесь расхождения нет.

## 5. Таблица: топ-10 по приоритету

| Команда | Проблема | Fix | Приоритет |
| --- | --- | --- | --- |
| `list`, `what` (каталог) | В `beacon list --format json` нет 10 рабочих команд, есть неработающая `up`. `what` не описывает рабочие команды и описывает мёртвую | Собрать каталог из одного источника с dispatch; подключить `up` или убрать из каталога; добавить тест равенства каталога и dispatch | P0 |
| все команды | `<cmd> --help` печатает общий usage в stderr и даёт код 2. `task --help` и `spawn --help` дают «неизвестный флаг». Справки по команде нет | Зарегистрировать `fs.Usage` для каждой команды, обработать `flag.ErrHelp` как код 0 в stdout; единый формат справки | P0 |
| `registry list/check/sync`, `gc` | Код и флаги есть, dispatch их не знает. `beacon registry list` и `beacon gc` дают «неизвестная подкоманда» | Подключить раннеры в `runRegistry` и `dispatch` или удалить мёртвые файлы. Сверка с T5.53/55/61 | P1 |
| `orchestrate`, `workload`, `pr create` | При ошибке печатают результат в stdout и возвращают 2 | Не рендерить при `err != nil`; stdout пустой при ненулевом коде | P1 |
| `logs`, `inspect`, `metrics`, `list` | Три разных механизма выбора формата: `--format table\|json`, `--format json\|prometheus`, `--json`. `logs --format` вообще не принимается | Один `--format` с перечислением значений на команду; `--json` оставить как псевдоним без разных семантик | P1 |
| `task`, `spawn`, остальные | Одинаковая ошибка валидации даёт 1 в `task` и 2 в остальных CLI | Зафиксировать правило в ADR 0016 или в CLI-спецификации и применить одинаково | P1 |
| `logs --json` | Флаг обещает JSON, но пропускает строки файла как есть. На выходе смесь JSON и logfmt | Парсить запись и кодировать в JSON независимо от `BEACON_LOG_FORMAT` | P1 |
| `daemon`, `up` | `--interval` описан тремя способами. `daemon --once` всегда возвращает 0. `up --once` недостижим | Один источник значения, передать результат `ReconcilePass` в код выхода, обновить README и usage | P2 |
| `registry stop`, `list`, `task list` | Двойной префикс, английский текст внутри русских ошибок, отсутствие имени команды и ожидаемых значений | Централизованный `cliError(cmd, err)`, один язык, список допустимых значений | P2 |
| `deliver`, `human`, `inspect`, `doctor`, `workload` | Только ад-hoc строковый вывод. У `deliver` есть JSON-теги, но они не используются | Добавить `--format json` и кодировать структуру; для `inspect` заменить `null` на `[]` | P2 |

## Пять главных находок

1. Машинный каталог команд расходится с работающими командами сильнее, чем кажется. В `beacon list --format json` нет `ci`, `daemon`, `pr`, `metrics`, `workload`, `deliver`, `orchestrate`, `orchestrator`, `registry`, `test`. Зато там есть `up`, который падает с «неизвестная подкоманда». Это ломает основной сценарий агента: прочитать каталог и вызвать команду по имени.

2. Справки по командам фактически нет. `--help` на любой команде даёт общий usage в stderr и код 2, а `task --help` и `spawn --help` дают ошибку парсера. README рекомендует `beacon what <command>`, но `what` знает лишь 18 команд из того же неполного каталога.

3. Три команды печатают машинный результат при ненулевом коде выхода. `orchestrate`, `workload` и `pr create` пишут причину в stderr, но рендерят результат в stdout и возвращают 2. Это нарушает ADR 0016 и может привести к тому, что агент прочитает результат как успешный.

4. Флаг `--format` перестал быть единым соглашением. Часть команд принимает `table|json`, `metrics` принимает `json|prometheus`, `logs` и `inspect` используют `--json` с разной семантикой, `logs --format json` не парсится вовсе. `--json` в `logs` не гарантирует JSON на выходе.

5. Документы обещают флаги и поведение, которых в коде нет. `--project` описан в четырёх документах, но не реализован. `workload --project|--all` предписан ADR 0156 и T3.383. README называет интервал демона тремя минутами против адаптивных 10s/30s в коде и 10s|30s в usage. Там же сказано, что `beacon human` не работает, хотя он подключён.

# Согласованность документации и карточек с кодом

Снимок: `origin/main` на коммите `b693e56d`. Проверены batch JSON, `docs/tasks`, генераторы индекса, ADR, `AGENTS.md` и workflow `docs.yml`. Production-код не менялся. Отчёт не повторяет `docs-consistency-gaps.md`, `docs-health-audit.md`, AUD-14, BCX-01/04/09/12/20/75 и исследование CLI UX.

## Итог

| ID | Приоритет | Разрыв | file:line и символ | Авторитетный источник |
| --- | --- | --- | --- | --- |
| DC-01 | P0 | Batch JSON расходится со статусом карточек на 76 карточках | `docs/research/batches/bcx.json:290` `BCX-14`, `docs/tasks/BCX-14.md:2` `Статус: todo`; `docs/batches/atx/atx.json:13` `ATX-01`, `docs/tasks/ATX-01.md:2` `Статус: done` | `AGENTS.md:56`, `internal/infra/batchapply/apply.go:651` |
| DC-02 | P0 | Индекс не видит 248 карточек и печатает 1 todo | `tools/beacon-gates/cardindex/check.go:39` `idPattern`, `docs/ROADMAP_STATUS.md:10` `Осталось реальных todo` | `AGENTS.md:56`, `internal/infra/taskstore/store.go:103` |
| DC-03 | P1 | ADR 0306 отсутствует, но на него ссылаются 45 раз | `docs/tasks/AUTOSP-02.md:7` `Ссылка: ADR 0306`, `docs/research/audit-01-batch.json:7` `number: 306` | `docs/adrs/` содержит 312 файлов, диапазон 0001-0313 без 0306 |
| DC-04 | P1 | ADR 0247 обещает check-md-links и md-lint в docs.yml | `docs/adrs/0247-docs-as-separate-gate.md:11`, `.github/workflows/docs.yml:31-43` | Workflow содержит только два шага `go run ./docsgate` |
| DC-05 | P2 | ADR 0257 ссылается на несуществующий SCHEMA.md и спорит с правилом batch-apply | `docs/adrs/0257-research-workflow.md:7`, `:15`, `docs/tasks/HD-122.md:11` | `AGENTS.md:3`, `:17`, `internal/infra/batchapply/apply.go:109` |

## DC-01. Статусы в batch JSON и карточках

Batch JSON хранит статус, но не синхронизирован с карточками. В `docs/research/batches/bcx.json` 79 карточек имеют `done`, 2 имеют `dropped`, 1 остаётся `todo`. В `docs/tasks/BCX-*.md` 40 карточек имеют `done`, 1 имеет `dropped`, 41 остаётся `todo`. Расхождение составляет 40 карточек. Пример: `docs/research/batches/bcx.json:290` объявляет `BCX-14` как `done` со ссылкой на PR 1277, а `docs/tasks/BCX-14.md:2` хранит `Статус: todo`.

В `docs/batches/atx/atx.json` все 44 карточки имеют `todo`. В `docs/tasks/ATX-*.md` 36 карточек имеют `done`. Расхождение составляет 36 карточек. Пример: `docs/batches/atx/atx.json:13` объявляет `ATX-01` как `todo`, а `docs/tasks/ATX-01.md:2` хранит `Статус: done`.

Карточка является источником истины. `AGENTS.md:56` прямо называет `docs/tasks/` источником истины и поручает менять статус Beacon при мерже. Генератор `internal/infra/batchapply/apply.go:651` берёт статус изbatch и пишет его в карточку в `:712`. Повторный batch-apply перезапишет 40 карточек BCX и сделает их `done` без решения владельца. История подтверждает намерение синхронизировать: коммиты `2afaccc4`, `90fbadb9` и `346638e9` правили именно `bcx.json` под закрытые карточки. Файл `atx.json` не правился с момента создания.

Предложение: сделать batch JSON производным от `docs/tasks/` или добавить проверку равенства статусов перед batch-apply. Пока проверки нет, не запускать batch-apply по этим двум файлам.

## DC-02. Индекс теряет карточки целых семейств

Оба генератора используют один шаблон ID. `tools/beacon-gates/cardindex/check.go:39` и `tools/roadmap-sync/main.go:38` содержат `idPattern` с семействами BS, HD, T, S, WIRING, UI, RS, Q, CLEAN и ARDH. В шаблоне нет ATX, BCX, IDX, AUD, AUTOSP, TUI, MEM, PROTO, BVF и INTG.

Замер по `docs/tasks/*.md`: 1253 файла, индекс видит 1005, теряет 248. Из потерянных 151 карточка имеет статус отличный от `done` и `dropped`. Среди видимых таких карточек 5. При этом `docs/ROADMAP_STATUS.md:10` печатает `Осталось реальных todo: 1`, а `:12` утверждает, что числа считает cardindex.

Конфликт с кодом уже есть. Коммит `a89fb203` добавил `letterDashIDRE` в `internal/infra/taskstore/store.go:103` и научил `beacon_task_claim` и `beacon_task_close` принимать ID вида `ATX-R01`. Карточки этого семейства стали допустимыми для агента, но оба индекса по-прежнему их не видят. `AGENTS.md:56` требует пересобирать индекс из карточек, а не из подмножества семейств.

Предложение: вынести описание ID в общий источник и использовать его в taskstore, cardindex и roadmap-sync. Пока это не сделано, печатать в `ROADMAP_STATUS.md` явный список семейств, которые индекс не считает.

## DC-03. ADR 0306 объявлен в batch, но файла нет

`docs/research/audit-01-batch.json:7` объявляет ADR с номером 306. Карточки `AUTOSP-02` до `AUTOSP-12` и `TUI-01` до `TUI-08` пишут `Ссылка: ADR 0306` в строке 7 своего файла. То же число встречается в `evals/main.go:942`, `evals/main.go:3215` и `evals/README.md:119`. Всего 45 ссылок в batch JSON, карточках и eval-коде.

В `docs/adrs/` лежит 312 файлов. Номера идут от 0001 до 0313, и 0306 единственный пропуск. Соседние ADR 0307 и 0308 существуют. Batch `f25294ba` создал ADR 0308 до 0313, но пропустил 0306 и не создал файл из batch `audit-01`.

Предложение: создать `docs/adrs/0306-*.md` из объявленного ADR в `docs/research/audit-01-batch.json:7-13` или перенаправить 45 ссылок на существующий номер. Проверка существования ADR в карточках не ловит пропуск, поэтому её стоит добавить в docsgate.

## DC-04. ADR 0247 обещает инструменты, которых нет в workflow

`docs/adrs/0247-docs-as-separate-gate.md:11` пишет, что docs-only PR идёт в `docs.yml` с `check-md-links` и `md-lint`, и что self-test доказывает падение обоих на плохом вводе. Фактический `.github/workflows/docs.yml` содержит два шага: `:31` self-test `go run ./docsgate --self-test` и `:38` проверку `go run ./docsgate -root ../.. -baseline ...`. Поиск `check-md-links|md-lint` по `.github` даёт 0 совпадений. Единственное упоминание вне ADR есть в `docs/tasks/HD-086.md:13` как описание Harbor.

ADR описывает чужую практику как принятое решение Beacon. Читатель ждёт проверки ссылок в docs gate, а её нет. Это подтверждает и `docs-consistency-gaps.md`, где отмечено, что `CheckLinks` смотрит только inline links.

Предложение: переписать ADR 0247 под реальный набор шагов docs.yml или добавить отсутствующие проверки и self-test для них.

## DC-05. ADR 0257 спорит с AGENTS.md и ссылается на отсутствующий файл

`docs/adrs/0257-research-workflow.md:7` утверждает, что SCHEMA.md требует `source_reports` и строк короче 600 символов. Файла SCHEMA.md нет в репозитории. Реальная схема живёт в структурах `internal/infra/batchapply/apply.go:109` до `:127`. Та же ссылка повторяется в `docs/tasks/HD-122.md:11`.

`docs/adrs/0257-research-workflow.md:15` пишет, что оркестратор применяет batch одной командой. `AGENTS.md:17` запрещает batch-apply без команды владельца, а `AGENTS.md:12` запрещает research-агенту вызывать execute и инструменты Beacon. `AGENTS.md:3` говорит, что этот файл важнее старых карточек и ADR. Статус ADR 0257 указан как `open` в строке 3, поэтому решение не считается принятым.

Предложение: заменить ссылку на SCHEMA.md путём кода и уточнить в ADR 0257, что batch применяется только после команды владельца.

## Как воспроизвести замеры

Команды выполняются из корня репозитория без сборки и тестов.

```sh
python3 -c "import json,collections; [print(p, collections.Counter(c['status'] for c in json.load(open(p))['cards'])) for p in ['docs/research/batches/bcx.json','docs/batches/atx/atx.json']]"
rg -n '^Статус:' docs/tasks/BCX-*.md docs/tasks/ATX-*.md
rg -n 'idPattern' tools/beacon-gates/cardindex/check.go tools/roadmap-sync/main.go
rg -n 'Осталось реальных todo|Три числа выше' docs/ROADMAP_STATUS.md
rg -n 'ADR 0306' docs/tasks docs/research evals
rg -n 'check-md-links|md-lint' .github docs/adrs/0247-docs-as-separate-gate.md
rg -n 'SCHEMA.md|применяет batch' docs/adrs/0257-research-workflow.md docs/tasks/HD-122.md
```

Замеры 76, 248, 151, 45 и 312 получены чтением файлов и не включают этот отчёт и его batch.

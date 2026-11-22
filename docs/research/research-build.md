# Разрывы в сборке, манифестах и инструментах гейтов

Дата: 2026-11-22. Проверено по `origin/main` на коммите `b693e56d8405cb7ef62898e0870c3f7fc5f75c0c`. Ветка перебазирована на `ad9f3889b285e20e643c9d1867862340f65045e4`. Два новых коммита main меняют только отчёты исследований и не задевают доказательную базу. Источники: чтение файлов, `git ls-files`, `git log`, `rg`, `sha256sum`, `go version -m` и побайтовое сравнение внедрённых JSON с закоммиченным бинарником. Сборки, тесты, гейты и `batch-apply` не запускались. Каждое утверждение опирается на путь, номер строки и символ.

## Сводка

| ID | Разрыв | Доказательство | Предложение | Приоритет |
| --- | --- | --- | --- | --- |
| BL-01 | Локальная проверка ждёт 7 инструментов MCP, хотя контракт описывает 10 описателей плюс wake-инструмент | `scripts/verify-beacon.sh:142-143`, `docs/runbooks/verification.md:57`, `internal/infra/mcp/contract_test.go:746-752`, `internal/infra/mcp/server.go:105`, `AGENTS.md:115` | Читать ожидаемое число из `AdvertisedToolNames()` через отдельный дамп в раннере и убрать литерал 7 из скрипта и ранбука | P1 |
| BL-02 | `docs/ROADMAP_STATUS.md` лежит в `.gitignore`, но остаётся отслеживаемым и регулярно правится вручную | `.gitignore:23-25`, `docs/tasks/IDX-11.md:2-3,33`, `docs/research/BATCH-VALIDATION.md:21`, `internal/app/orchestrator/decision.go:79-80`, `git ls-files docs/ROADMAP_STATUS.md` | Выбрать одну политику: убрать путь из ignore и из списка генерируемых, либо разотследить файл и убрать ручные правки карточек | P2 |
| BL-03 | Флаги воспроизводимой сборки `beacon` скопированы в четыре места, и только бинарник гейтов имеет проверку байт в байт | `.github/workflows/ci.yml:43-47`, `.github/workflows/ci.yml:330-334`, `.github/workflows/tests.yml:37-41`, `tools/perf/measure.sh:736-737`, `.github/workflows/gates-binary.yml:41-59` | Вынести флаги в один composite-экшен или сценарий и сравнивать хеш `beacon` между тремя джобами | P1 |
| BL-04 | Третий Go-модуль `evals` с собственным `go.mod` и `go.sum` не собирается и не тестируется в CI | `evals/go.mod:1-30`, `evals/README.md:36-40`, `.github/workflows/ci.yml:67-78`, `rg -n evals .github/` | Добавить шаг `cd evals && go test -tags=eval ./...` в обязательную джобу и проверять, что версия Go совпадает с корневым модулем | P2 |
| BL-05 | Гейты выполняются из двух источников: часть из закоммиченного бинарника, часть через `go run` | `.github/workflows/ci.yml:246-249`, `.github/workflows/ci.yml:257`, `.github/workflows/ci.yml:303`, `.github/workflows/docs.yml:36,43`, `tools/beacon-gates/main.go:24-31` | Либо выполнять все шесть зарегистрированных гейтов из бинарника, либо явно разделить контракты и убрать ложное ощущение одного артефакта | P2 |

P0 нет. Ни один найденный разрыв не ломает main на момент проверки.

## P1

### BL-01. Устаревший контракт числа инструментов в локальной проверке

Скрипт `scripts/verify-beacon.sh:142-143` содержит условие `len(tools.Tools) != 7` и сообщение с ожидаемым числом 7. Ранбук повторяет это число в `docs/runbooks/verification.md:57`. Одновременно контракт в `internal/infra/mcp/contract_test.go:746-752` фиксирует 11 advertised names: десять description tools плюс `WakeToolName`. Комментарий в `internal/infra/mcp/server.go:105` перечисляет ten tools. Список в `AGENTS.md:115` называет десять инструментов. Текущий прогон `verify-beacon.sh` отвергнет рабочий сервер как сломанный, потому что получит 10 или 11 инструментов вместо семи.

Скрипт дублирует и версии манифеста. Строка `scripts/verify-beacon.sh:94` задаёт `go 1.26.0`, строка `:96` задаёт `require github.com/modelcontextprotocol/go-sdk v1.8.0`. Источники лежат в `go.mod:3` и `evals/go.mod:3,6`. Скрипт копирует `evals/go.sum` в строке `:90`, но версию требования не выводит из манифеста. Повышение версии SDK в `evals/go.mod` оставит проверку на старой версии без сигнала расхождения.

Предложение: добавить в раннер команду, которая печатает число из `AdvertisedToolNames()`, и сравнить его с ответом дочернего процесса. Версию Go и SDK брать из `go.mod` и `evals/go.mod` во время выполнения скрипта.

### BL-03. Одинаковые флаги сборки без общего определения

Один и тот же набор `CGO_ENABLED=0 -trimpath -buildvcs=false -ldflags="-buildid= -s -w"` записан в `.github/workflows/ci.yml:43-47`, в `.github/workflows/ci.yml:330-334`, в `.github/workflows/tests.yml:37-41` и в `tools/perf/measure.sh:736-737`. Документация дублирует его в `.github/workflows/README.md:24,42`, `docs/adrs/0016-build-system.md:28` и `docs/adrs/0023-gates-binary-toolchain.md:9`.

Проверка `.github/workflows/gates-binary.yml:41-59` сравнивает байты только для `beacon-gates`. Бинарник `beacon` собирают три джобы независимо, и ни одна не сравнивает его хеш с другой. Правка флагов в одной джобе останется незамеченной, пока не сломает поведение. ADR 0023 описывает инструмент как источник расхождений хешей, но мера защиты покрывает только гейты.

Предложение: завести общий composite-экшен или сценарий с флагами, вызывать его во всех четырёх местах и добавить печать SHA-256 `beacon` в трёх джобах с сравнением результата.

## P2

### BL-02. Отслеживаемый файл в списке игнорируемых

Файл `.gitignore:23-25` называет `docs/ROADMAP_STATUS.md` производным документом и запрещает его коммитить. Карточка `docs/tasks/IDX-11.md:2-3` фиксирует, что все три производных файла удалены из индекса. Подтверждение в `docs/tasks/IDX-11.md:33` повторяет, что файлы в git не попадают.

Фактически `git ls-files docs/ROADMAP_STATUS.md` возвращает путь. Файл правился в коммитах `f33f139d`, `9e29d9ff`, `87425fdf` и других. Документ `docs/research/BATCH-VALIDATION.md:21` считает три пути некоммитируемыми и разрешает их в `owner_files`. Код `internal/app/orchestrator/decision.go:79-80` держит тот же путь в `DefaultGeneratedFilePatterns` и отклоняет ручную правку в строке `:281`. Правило ignore не действует на отслеживаемый файл, поэтому политика живёт только в документах и расходится с деревом.

Предложение: оставить файл отслеживаемым, убрать его из `.gitignore` и из `DefaultGeneratedFilePatterns`, обновить IDX-11 и BATCH-VALIDATION. Либо разотследить файл и убрать правки счётчиков из карточек AUD-14, BCX-04, ATX-D06 и CLEAN-01.

### BL-04. Модуль evals без проверки в CI

Модуль `github.com/refusedguy/beacon/evals` объявлен в `evals/go.mod:1` и имеет собственный `go.sum`. Файл `evals/README.md:36-40` прямо говорит, что корневой `go test ./...` в него не заходит и что `evals/run.sh` остаётся единственной командой запуска. Джоба test в `.github/workflows/ci.yml:67-78` прогоняет только корневой модуль и модуль `tools/beacon-gates`. Поиск `evals` по `.github/` даёт одно совпадение в комментарии `.github/workflows/ci.yml:357`.

Скрипт `scripts/verify-beacon.sh:90` копирует `evals/go.sum` во временный модуль. Если зависимости `evals` разойдутся с корневым модулем, локальная проверка может упасть, а CI этого не покажет.

Предложение: добавить отдельный шаг с `cd evals && go test -tags=eval ./...` и сравнить директиву `go` всех трёх модулей в одной проверке.

### BL-05. Два источника выполнения гейтов

Реестр в `tools/beacon-gates/main.go:24-31` содержит шесть команд, включая `tool-contract`. Джоба gates в `.github/workflows/ci.yml:246-249` прогоняет self-test только для четырёх команд из закоммиченного бинарника. Self-test `tool-contract` идёт через `go run .` в `.github/workflows/ci.yml:257`, то есть из исходников. Индекс карточек запускается через `go run ./cardindex` в `.github/workflows/ci.yml:303`. Docs gate запускается через `go run ./docsgate` в `.github/workflows/docs.yml:36,43`.

Проверка `.github/workflows/gates-binary.yml:41-59` гарантирует совпадение бинарника с исходниками, но не гарантирует, что CI выполняет именно этот бинарник. Для `tool-contract`, `cardindex` и `docsgate` артефактом выступает компиляция на раннере. Комментарий `.github/workflows/ci.yml:284` называет отдельный main package известным фактом, поэтому это не новый баг, а незакрытый контракт.

Предложение: выполнять `tool-contract` из бинарника и явно документировать, что `cardindex` и `docsgate` не входят в него. Тогда path-фильтр `gates-binary.yml` перестанет требовать пересборку при правках этих двух пакетов.

## Проверено без разрыва

Внедрённые конфиги совпадают с файлами на диске побайтово. Проверка читала `go:embed` в `tools/beacon-gates/gate/layers.go:20`, `tools/beacon-gates/gate/reachability_exceptions.go:13`, `tools/beacon-gates/gate/exceptions.go:11,14`. Содержимое `layers.json`, `reachability.json`, `baseline.json` и `permanent.json` найдено в бинарнике `tools/beacon-gates/beacon-gates` как точная байтовая подпоследовательность. Файлы `docs-baseline.json` и `registry.json` внедрены не были и через `go:embed` не подключаются.

Версия Go одинакова во всех трёх модулях: `go.mod:3`, `tools/beacon-gates/go.mod:3`, `evals/go.mod:3`. Директив `toolchain` нет. `go version -m tools/beacon-gates/beacon-gates` сообщает `go1.26.0`, `-trimpath=true`, `CGO_ENABLED=0` и не показывает `GOEXPERIMENT`.

Слои в `tools/beacon-gates/gate/config/layers.json:3-13` соответствуют существующим каталогам. Список `allowed` в строках `:16-24` содержит ребро `cmd -> internal/tui`, добавленное после WIRING-915.

## Ограничения

Гейты, тесты, локальные сборки и `batch-apply` не запускались по правилам `AGENTS.md:10`. Вывод о счётчике инструментов получен чтением контракта и скрипта, без запуска `beacon mcp`. Байтовое совпадение внедрённых конфигов проверено на закоммиченном бинарнике без пересборки из исходников. Проверка branch protection не выполнялась, этот вопрос закрыт в `docs/research/ci-gates-gaps.md:22`.

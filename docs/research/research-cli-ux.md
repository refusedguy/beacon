# Разрывы CLI UX: каталог, dispatch, doctor, документация

Дата: 2026-11-22. Коммит: `b693e56d` (`origin/main`). Область чтения: `cmd/beacon/catalog.go`, `cmd/beacon/commands.go`, `cmd/beacon/doctor.go`, `cmd/beacon/main.go`, тесты в `cmd/beacon`, `README.md`, `docs/RULES-CHECK-CONTRACT.md`, `go.mod`. Production-код не менялся. `go test`, `go build`, `beacon gates` и `batch-apply` не запускались. Бинарник не собирался. Выводы основаны на чтении кода на этом коммите, а не на измеренном выводе команды.

## Границы

Отчёт не повторяет `docs/research/cli-ux-audit.md`, карточки BCX-21, BCX-22, BCX-23, BCX-24, BCX-25, BCX-28, BCX-30, BCX-68, BCX-76, AUD-10 в `AGENTS.md` и проверку совпадения `catalog.go` с `runnerFor`. Последняя закрыта тестом `TestCatalogMatchesDispatch` (`cmd/beacon/catalog_completeness_test.go:9-31`), который сверяет множества имён в обе стороны. Настоящий отчёт ищет разрывы в содержимом каталога, в разборе аргументов и в проверках doctor.

## Таблица находок

| ID | Разрыв | Доказательство | Приоритет |
| --- | --- | --- | --- |
| CU-01 | Usage-строки каталога и `usageText` не описывают флаги команд. У `doctor` режим `--watch` скрыт полностью | `cmd/beacon/catalog.go:41` (`catalog`, Usage `beacon doctor`), `cmd/beacon/doctor.go:36-39` (флаги), `cmd/beacon/doctor.go:43-50` (`runDoctorWatch` до проверок), `cmd/beacon/main.go:29` (`usageText`), `cmd/beacon/catalog.go:86` (`writeCommandHelp`) | P0 |
| CU-02 | `hasHelpFlag` не останавливается на `--` и перехватывает `--help` аргументов внешней проверки | `cmd/beacon/main.go:330` (`dispatch`), `cmd/beacon/main.go:348-354` (`hasHelpFlag`), `cmd/beacon/doctor.go:69-76` (`splitDoctorWatch`), `cmd/beacon/doctor.go:136-154` (`check`) | P0 |
| CU-03 | Словарь статусов doctor расходится с контрактом, а `runDoctor` считает только `FAIL` | `cmd/beacon/doctor.go:24-26` (комментарий `checkResult`), `docs/RULES-CHECK-CONTRACT.md:9,56`, `cmd/beacon/doctor.go:197,205,220` (`OK`), `cmd/beacon/doctor.go:56` (`ch.status == "FAIL"`) | P1 |
| CU-04 | `commandFormats` назван единым реестром, но содержит три команды из тринадцати с `--format` | `cmd/beacon/catalog.go:108-115` (`commandFormats`), `cmd/beacon/catalog.go:133-159` (`runList`, свой `switch`), `cmd/beacon/format_unified_test.go:10-38`, `cmd/beacon/workload.go:125` | P1 |
| CU-05 | Проверки doctor поверхностны и не имеют тестов | `cmd/beacon/doctor.go:20,22,190-195` (`checkGo`), `cmd/beacon/doctor.go:230-248` (`findGates`), `cmd/beacon/doctor.go:52,208-209,293-307` (`checkStateDir`, `stateDir`), `cmd/beacon/doctor_test.go` (нет тестов проверок) | P2 |

## P0

### CU-01. Каталог и usageText скрывают рабочие флаги

Запись `doctor` в `catalog` (`cmd/beacon/catalog.go:41`) содержит `Usage: "beacon doctor"` и `Summary` про три проверки: версию Go, `beacon-gates` и каталог состояния. В `runDoctor` (`cmd/beacon/doctor.go:33-64`) определены четыре флага: `--watch`, `--interval`, `--session`, `--max-ticks` (`cmd/beacon/doctor.go:36-39`). Ветка `if *watch` (`cmd/beacon/doctor.go:43-50`) возвращает результат `runDoctorWatch` раньше, чем список проверок `c.checkGo(), c.checkGates(), c.checkStateDir()` (`cmd/beacon/doctor.go:52`). Режим `--watch` не выполняет ни одной проверки, обещанной в `Summary`.

`beacon what doctor` и `beacon doctor --help` читают ту же строку через `writeCommandHelp` (`cmd/beacon/catalog.go:83-91`), поэтому оба источника справки повторяют урезанный Usage. Общий `usageText` (`cmd/beacon/main.go:29`) тоже содержит только `beacon doctor`.

Та же проблема у трёх других команд. `agents` (`cmd/beacon/catalog.go:33`) обещает только `--format`, тогда как `runAgents` объявляет `--route`, `--handoff`, `--to`, `--task`, `--repo`, `--worktree` (`cmd/beacon/agents.go:144-149`). `watch` (`cmd/beacon/catalog.go:59`) не упоминает `--live`, который определён в `cmd/beacon/watch.go:215`. `attach` (`cmd/beacon/catalog.go:34`) описывает только `<sessionID>`, а `parseAttachArgs` принимает `--resume` (`cmd/beacon/attach.go:201-204`).

`TestCatalogMatchesDispatch` (`cmd/beacon/catalog_completeness_test.go:9-31`) сверяет только имена. Тест, который сверяет `list` с `what`, читает ту же строку `Usage` из каталога, поэтому расхождение с реальными флагами остаётся невидимым.

Владельцы файлов в карточке: `cmd/beacon/catalog.go`, `cmd/beacon/catalog_test.go`. Исправление меняет machine-каталог и его тест. Правка `usageText` в `main.go` отнесена к CU-02, чтобы файлы не пересекались в фазе.

### CU-02. `--help` во внешней проверке перехватывается диспетчером

`dispatch` (`cmd/beacon/main.go:323-343`) вызывает `hasHelpFlag(args[1:])` до запуска раннера (`cmd/beacon/main.go:330`). Функция `hasHelpFlag` (`cmd/beacon/main.go:348-354`) просматривает все аргументы и возвращает true на первом `-h` или `--help`, не обращая внимания на разделитель `--`.

`splitDoctorWatch` (`cmd/beacon/doctor.go:69-76`) отделяет аргументы после `--` и передаёт их как команду внешней проверки. Замыкание `check` (`cmd/beacon/doctor.go:136-154`) запускает эту команду через `c.run`. Для вызова `beacon doctor --watch -- <check> --help` диспетчер видит `--help` в аргументах проверки, вызывает `writeCommandHelp` (`cmd/beacon/main.go:331`) и возвращает 0 (`cmd/beacon/main.go:336`). Вотчер не запускается, а агент получает справку вместо события wake.

Тест `TestW115CLIEmitsOneEventAndNoModelCalls` (`cmd/beacon/doctor_test.go:151-184`) передаёт внешней проверке аргументы `--mode ci` (`cmd/beacon/doctor_test.go:163`) и не содержит `--help`. Негативный случай не покрыт.

Владельцы файлов в карточке: `cmd/beacon/main.go`, `cmd/beacon/main_test.go`.

## P1

### CU-03. Статусы doctor не совпадают с контрактом проверок

Комментарий к `checkResult` (`cmd/beacon/doctor.go:24-26`) сообщает, что статусы используют тот же верхний регистр, что и отчёт правил: `PASS`, `WARN`, `FAIL`, `BROKEN`. Контракт `docs/RULES-CHECK-CONTRACT.md:9` задаёт ровно эти четыре значения, а `docs/RULES-CHECK-CONTRACT.md:56` требует, чтобы поле `status` и human-вердикт использовали один регистр.

Реализация выдаёт только `OK` (`cmd/beacon/doctor.go:197`, `:205`, `:220`) и `FAIL` (`cmd/beacon/doctor.go:181`, `:187`, `:195`, `:203`, `:211`, `:218`). Значения `PASS`, `WARN` и `BROKEN` не производятся ни одной проверкой.

Вторая часть разрыва находится в `runDoctor` (`cmd/beacon/doctor.go:54-59`): флаг `failed` выставляется только при `ch.status == "FAIL"` (`cmd/beacon/doctor.go:56`). Если проверка вернёт `BROKEN`, цикл напечатает строку, но `failed` останется false и команда вернёт 0 (`cmd/beacon/doctor.go:63`). По контракту `BROKEN` блокирует (`docs/RULES-CHECK-CONTRACT.md:17,19`). Для сравнения, команда `rules` печатает машинный fence `aif-gate-result` (`cmd/beacon/commands.go:84-89`), а doctor печатает только human-строки (`cmd/beacon/doctor.go:55`).

Владельцы файлов в карточке: `cmd/beacon/doctor.go`, `cmd/beacon/doctor_test.go`.

### CU-04. Единый реестр форматов покрывает три команды

Комментарий `commandFormats` (`cmd/beacon/catalog.go:108-110`) называет карту единственным перечислением значений `--format` для каждой команды и утверждает, что валидация и тесты читают её. Карта (`cmd/beacon/catalog.go:111-115`) содержит только `inspect`, `logs`, `metrics`.

`runList` (`cmd/beacon/catalog.go:133-159`) не вызывает `parseCommandFormat`. Он проверяет `*format` собственным `switch` (`cmd/beacon/catalog.go:140-159`) и печатает собственное сообщение об ошибке (`cmd/beacon/catalog.go:156`). Тесты `TestCommandFormatsAcceptDeclaredValues` и `TestCommandFormatsRejectUnknownValue` (`cmd/beacon/format_unified_test.go:10-38`) перебирают только ключи карты.

Остальные команды объявляют `--format` самостоятельно: `agents` (`cmd/beacon/agents.go:143`), `watch` (`cmd/beacon/watch.go:216`), `gc` из `watch.go:819`, `workload` (`cmd/beacon/workload.go:125`), `registry list` (`cmd/beacon/registry_list.go:112`), `orchestrate` (`cmd/beacon/orchestrate.go:226`), `pr create` (`cmd/beacon/prcreate.go:295`), `task list` (`cmd/beacon/task.go:169`). Каталог рекламирует для них `table|json` (`cmd/beacon/catalog.go:33,39,43-44,51,59,62`), но центральная проверка их не видит.

Владельцы файлов в карточке: `cmd/beacon/catalog.go`, `cmd/beacon/format_unified_test.go`.

## P2

### CU-05. Проверки doctor поверхностны и не покрыты тестами

`checkGo` (`cmd/beacon/doctor.go:178-198`) сравнивает только старший и младший номер версии буквальным числом `26` (`cmd/beacon/doctor.go:194`). Константа `minGoVersion = "1.26.0"` (`cmd/beacon/doctor.go:20`) используется только в тексте ошибки (`cmd/beacon/doctor.go:195`). Третья группа regexp (`cmd/beacon/doctor.go:22`) захватывает patch-версию, но не читается. `README.md:11` утверждает, что версия зафиксирована в `go.mod` и проверяется doctor. В `go.mod:3` записано `go 1.26.0`. Изменение `go.mod` не меняет порог проверки.

`checkGates` (`cmd/beacon/doctor.go:200-206`) получает путь через `findGates` (`cmd/beacon/doctor.go:230-248`). Явный путь из `BEACON_GATES` проверяется только через `os.Stat` и `IsDir` (`cmd/beacon/doctor.go:231-236`). Бит исполнения не проверяется. `runGates` (`cmd/beacon/commands.go:97-105`) передаёт этот путь в `c.run`, поэтому неисполняемый файл даёт `OK` в doctor и отказ в `gates`.

`checkStateDir` (`cmd/beacon/doctor.go:208-221`) вызывает `stateDir()` без аргумента (`cmd/beacon/doctor.go:209`). Функция `stateDir` (`cmd/beacon/doctor.go:293-327`) принимает вариативный корень (`cmd/beacon/doctor.go:302-307`), но doctor его не передаёт. Проверка gates использует `c.root` (`cmd/beacon/doctor.go:201`), который вычислен через `findModuleRoot` (`cmd/beacon/main.go:213`, `cmd/beacon/main.go:439-455`). Проверка состояния берёт корень из окружения или из текущего каталога (`cmd/beacon/doctor.go:301-326`). Внутри одной команды получаются два разных проекта.

В `cmd/beacon/doctor_test.go` нет тестов для `checkGo`, `checkGates` и `checkStateDir`. Файл покрывает `splitDoctorWatch` и wiring вотчера (`cmd/beacon/doctor_test.go:36-245`).

Владельцы файлов в карточке: `README.md`, `cmd/beacon/doctor.go`, `cmd/beacon/doctor_test.go`.

## Итог

Пять карточек. Две в P0, две в P1, одна в P2. Внутри каждой фазы владельцы файлов не пересекаются: P0 использует `catalog.go` с `catalog_test.go` и `main.go` с `main_test.go`, P1 использует `doctor.go` с `doctor_test.go` и `catalog.go` с `format_unified_test.go`, P2 использует `README.md` и doctor-файлы. Ни одна карточка не меняет production-код в этом отчёте.

# Research: зрелость eval-харнесса beacon

Дата: 22 ноября 2026. Снимок: `origin/main`, коммит `2d34645b`. Метод: чтение исходников и workflows. Тесты, сборка, gates и `batch-apply` не запускались, по правилам `AGENTS.md`.

## Границы

Отчёт читает `evals/`, `docs/evals/`, `.github/workflows/` и смежные аудиты. Он не повторяет `docs/research/research-e2e-coverage.md` (карточки EC-01..EC-05 про docs e2e, хаос-тест и триггеры e2e), `docs/research/metrics-audit.md` (дыры в рабочих счётчиках) и карточки RS-EVAL-01..06 (конкретные правки раннера). Здесь оценивается только зрелость измерительного контура: что выполняется, что считается, чего не хватает владельцу.

Файла `.github/workflows/evals.yml` в репозитории нет. Workflow с eval-шагами называется `tests.yml`, job `registry-check`.

## Что реально запускается в CI

| Проверка | Где | Что выполняется | Блокирует merge |
| --- | --- | --- | --- |
| `Go directives match across modules` | `tests.yml:41-58` | Сравнение директивы `go` в трёх `go.mod` | Да |
| `Test evals module` | `tests.yml:65-70` | `go test -mod=mod -tags=eval ./...` в каталоге `evals` | Да |
| `e2e` в `ci.yml` | `ci.yml:101-137` | `bash tests/e2e/run.sh`, реальный opencode 2.3.0 | Да |
| `e2e` в `e2e.yml` | `e2e.yml:34-80` | Тот же `tests/e2e/run.sh` | Нет, `continue-on-error: true` в `:41` |
| `seed-spec`, `registry-live`, `cardindex`, `rules-broken` | `ci.yml:142-369` | Тесты домена и store, индекс карточек, rules gate | Да |

Ключевой факт: `Test evals module` запускает только файлы `*_test.go` с build tag `eval`. Их пять: `evals/blocked_test.go`, `evals/status_test.go`, `evals/naming_test.go`, `evals/cmd/evalserver/severity_test.go`, `evals/cmd/evalserver/drift_test.go`. `go test` собирает пакет и вызывает функции `Test*`, но не вызывает `main()` из `evals/main.go`. Поэтому табличный раннер с кейсами 01_handshake, ..., SMOKE в CI не выполняется вообще.

В CI нет ни одного шага, который вызывает `go run -tags=eval .` или `bash evals/run.sh`. Единственный способ получить таблицу PASS/WARN/FAIL это локальный запуск по `evals/README.md:23-25`. Коммит `b32f955b` добавил eval-тесты в CI и прямо называет baseline "zero CI steps ran the evals module before this change". Он добавил юнит-тесты модели, а не прогон сценариев.

## Что умеет табличный раннер

`evals/main.go` поднимает eval-сервер через `mcp.NewServerWithDeps` с реальным SQLite-store во временном `XDG_STATE_HOME` (`evals/main.go:336-388`, `evals/cmd/evalserver/main.go:323-374`). Дальше он вызывает реальные обработчики MCP через официальный go-sdk `CommandTransport`. Свой JSON-клиент не написан.

Раннер регистрирует около 50 именованных кейсов. Группы:

- Протокол и состояние, `01_handshake` .. `08_cancel` (`main.go:393-602`). Проверяется форма ответов MCP: имя сервера, ровно восемь инструментов, поля `activeWatches`/`budget`/`ladder`/`persist`/`version`, наличие watchID в `list`, `ok=true` у ack и cancel.
- Чистая доменная модель, `09_ship_verdict`, `11_verdict_regression` (`main.go:604-629`). Verdict считается до и после чекпоинта и сравнивается с замороженной строкой baseline для семи сценариев.
- Живой плагин wake на фейковом хосте, `12` .. `15`, `16` .. `20` (`main.go:717-1016`). Харнесс на node импортирует `.opencode/plugins/beacon-bridge.js` и возвращает наблюдаемые факты. Проверяется steer, квитанция `wx 0600`, экранирование враждебного текста, transport-failed, ID из реального store.
- Исполняемые модели без production-кода: W14-W18 (авто-мерж ADR 0072), B01-B05 (blocked Watch), OW01-OW03 (self-awareness), D01-D04 (единое решение), EC01-EC02 (подтверждение лимита), N01-N04 (имена артефактов). Для них в `main.go` лежат эталонные функции `decideMerge`, `applyBlockDecision`, `decideOrchestrator` и другие. `evals/README.md:256` прямо говорит, что production-кода для мержа нет.
- Служебные: `R01_accepted_skip` (WARN), `CT01_contract_tools_frozen`, `FQ01_flaky_quarantine_limits`, `PC01_pass5_negative_control`, `SMOKE_start_reason` (`main.go:1024-1057`).
- W01-W13, живые wake-сценарии. Включаются только при `BEACON_WAKE_EVAL=1` и внешних песочницах (`main.go:850-857`, `evals/run.sh:35-47`).

## Метрики и пороги

Раннер считает три числа: `passed`, `warned`, `failed` (`main.go:117-141`, `:1060-1065`). Код выхода равен 1 при `failed > 0`. WARN не блокирует. Это единственный вердикт прогона.

Пороги в раннере бинарные:

- `passGate` требует ровно пять прогонов и пять успехов (`main.go:161-186`). Пять стохастических кейсов идут через него: 12, 13, 14, 15, 19, 20 (`evals/README.md:493-518`). Шаблон вроде `trials 4/5 (11101)` печатается в колонку ACTUAL.
- Замороженные хеши: `frozenContractToolsHash` в `main.go:47` и `frozenScenarioHash` в e2e-тесте. Правка списка без обновления константы даёт FAIL.
- Карантин FLAKY: не более пяти строк, срок не более семи дней (`main.go:208-211`, `:246-272`).
- `11_verdict_regression` сравнивает строки вердикта с baseline. Расхождение даёт FAIL.

Непрерывных метрик раннер не пишет. Нет ни длительности кейса, ни RSS, ни числа вызовов инструмента, ни стоимости, ни token usage. `docs/evals/e11-metrics-counters.md:16` ставит цель RSS 20-30 МБ, но это сценарий-спецификация, а не исполняемый eval. `docs/research/metrics-audit.md:74` показывает, что gauge `active_watches` и `outbox_depth` вызываются только в тестах и всегда нулевые.

## Сверка с нуждами владельца

| Нужда | Состояние | Доказательство |
| --- | --- | --- |
| Замеры перфа | Нет. Есть только бинарный PASS/FAIL и счётчики проходов | `main.go:1060-1065` печатает три числа; `e11-metrics-counters.md:16` остаётся целью |
| Воспроизводимость | Частичная. Временные XDG-каталоги на прогон, `GOPROXY=off`, без токенов. Но окружение не фиксируется в отчёте, headSHA заглушка, живой proof сделан вручную | `evals/README.md:33-34,74-76`, `:226-250`, R01 в `main.go:1024-1031` |
| Сравнение до/после | Только внутри одного процесса. `11_verdict_regression` сравнивает вердикт до и после чекпоинта. Нет baseline из `origin/main` и нет дельты между коммитами | `main.go:623-629`, `evals/README.md:135-145` |
| Детекция регрессий | Сильная по контракту, но не подключена к CI. Заморозка хешей, негативные контроли, pass^5 ловят регрессию только при локальном прогоне | `CT01`, `PC01`, `FQ01` в `main.go:1033-1049`; шагов в workflow нет |

Отдельная проблема: модель и раннер живут в одном модуле, но CI проверяет только модель. Если регрессия ломает сам табличный прогон, например падает сборка evalserver или меняется число кейсов в `checkTwoPhaseLocalCIGate`, required-проверка этого не увидит.

## Конкурентные аналоги

Источники: README через raw URL и статья Anthropic. `websearch` не использовался.

SWE-bench (`SWE-bench/SWE-bench`, README). Контейнеризированный harness на Docker для воспроизводимости. Результат пишется в `logs/evaluation/<run_id>/results.json`. Команда `swebench report <run_id>` перепроверяет сохранённые логи без контейнеров. Кэш результатов ключуется парой `run_id` и `instance_id`, поэтому повтор с другим diff требует нового `run_id`. Есть gold patches и проверенное подмножество Verified на 500 задач.

OpenAI Evals (`openai/evals`, README). Реестр eval с YAML-шаблонами, поддержка частных eval и логирование результатов во внешнее хранилище. Сейчас не принимает eval с пользовательским кодом.

Inspect AI (`UKGovernmentBEIS/inspect_ai`, README). Более 200 готовых eval, зависимости зафиксированы lockfile, для агентов опубликован структурированный индекс документации.

Promptfoo (`promptfoo/promptfoo`, README). CLI с первого класса интеграцией в CI/CD, кэширование результатов, сравнение моделей side by side, локальный режим без отправки данных.

Anthropic, Demystifying evals for AI agents (9 января 2026). Различает trial, grader, transcript и evaluation harness. pass@k считает вероятность хотя бы одного успеха за k попыток, pass^k вероятность что все k успешны. При 75 процентов на trial и трёх trials pass^k около 42 процентов. Capability-сьюты начинаются с низкого pass rate, regression-сьюты должны держаться около 100 процентов и блокировать. Каждый trial Starts from clean environment. Рекомендуется читать transcript'ы и хранить артефакты, чтобы отличать ошибку агента от ошибки grader. Перечислены Harbor, Braintrust, LangSmith, Langfuse и Arize. Harbor и есть образец, на который ориентируется `evals/README.md:3`.

Что взять:

1. Запускать раннер в CI как отдельную обязательную проверку. Сейчас его там нет.
2. Писать машиночитаемый отчёт с run_id и версиями инструментов, как `results.json` в SWE-bench.
3. Хранить транскрипты провальных trials, чтобы разбирать флаки по фактам, а не по шаблону `11101`.
4. Разделить capability и regression по блокируемости. pass^5 уже реализован как мера консистентности.
5. Сравнивать прогон с baseline из `origin/main` и печатать дельту, а не только бинарный verdict.

## Находки

| ID | Фаза | Утверждение | Доказательство |
| --- | --- | --- | --- |
| EM-01 | P0 | Табличный eval-раннер не выполняется в CI, обязательная проверка покрывает только юнит-тесты модели | `tests.yml:65-70`; `evals/main.go:318-322`; пять `*_test.go` с build tag eval |
| EM-02 | P1 | Прогон не оставляет машиночитаемого отчёта, только таблицу в stdout | `main.go:1059-1065`; в `main.go` нет записи отчёта на диск |
| EM-03 | P1 | Нет замеров перфа: длительности кейсов, RSS и числа вызовов инструментов | `main.go:108-141`; `e11-metrics-counters.md:16`; `metrics-audit.md:74` |
| EM-04 | P1 | Нет сравнения с baseline из главной ветки, только внутрипроцессный чекпоинт вердикта | `main.go:623-629`; `evals/README.md:135-145` |
| EM-05 | P1 | Окружение прогона не фиксируется в отчёте, живые сценарии опираются на ручной proof | `evals/README.md:33-34,226-250`; R01 в `main.go:1024-1031` |
| EM-06 | P2 | pass^5 печатает шаблон прогонов, но не сохраняет наблюдения провального trial | `main.go:161-186`, `:743-757`; `evals/README.md:493-518` |
| EM-07 | P2 | W01-W13 выключены по умолчанию и не имеют герметичного аналога с наблюдаемыми артефактами | `evals/run.sh:35-47`; `evals/README.md:283-325` |
| EM-08 | P2 | Все кейсы имеют один порог FAIL, нет разделения capability и regression | `main.go:69-92`, `:1060-1065`; Demystifying evals for AI agents, раздел про capability и regression |

Карточки с измеримыми проверками лежат в `docs/research/research-eval-maturity-batch.json`.

## Источники

- `evals/main.go`, `evals/go.mod`, `evals/README.md`, `evals/run.sh`, `evals/blocked_test.go`
- `evals/cmd/evalserver/main.go`, `evals/cmd/evalserver/drift.go`
- `docs/evals/e11-metrics-counters.md`, `docs/evals/mem-footprint-01.md`
- `.github/workflows/tests.yml`, `.github/workflows/ci.yml`, `.github/workflows/e2e.yml`, `.github/workflows/docs.yml`
- `docs/research/metrics-audit.md`, `docs/research/research-e2e-coverage.md`
- Коммит `b32f955b` (BL-04): добавление eval-тестов в CI
- https://raw.githubusercontent.com/SWE-bench/SWE-bench/main/README.md
- https://raw.githubusercontent.com/openai/evals/main/README.md
- https://raw.githubusercontent.com/UKGovernmentBEIS/inspect_ai/main/README.md
- https://raw.githubusercontent.com/promptfoo/promptfoo/main/README.md
- https://www.anthropic.com/engineering/demystifying-evals-for-ai-agents

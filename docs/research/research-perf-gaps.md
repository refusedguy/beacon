# Исследование: пробелы в замерах производительности Beacon

Дата: 2026-11-22. База: origin/main, коммит `2d34645b`. Ветка: `docs/perf-gaps-research`.

## Вопрос владельца

Владелец говорит, что замеров перформанса нет. Часть замеров в репо есть, и они работают. Две причины создают ощущение пустоты. `docs/evals/e11-metrics-counters.md` ставит цель 20-30 MB и прямо называет её целью. `docs/perf/README.md` фиксирует пороги SLO, но таблица результатов заполнена прочерками. Ни один документ не показывает фактические значения гистограмм, которые уже есть в коде.

Задача исследования: найти места замеров в origin/main, отделить работающие метрики от отсутствующих, снять доступные числа и записать разрывы в виде карточек.

## Что уже считается в коде

| Место | Что делает | Тип |
| --- | --- | --- |
| `internal/infra/observe/observe.go:230-233` | имена четырёх гистограмм: `poll_cycle_duration_seconds`, `merge_duration_seconds`, `wake_duration_seconds`, `run_duration_seconds` | определение |
| `internal/infra/observe/observe.go:444-455` | `PollCycleEnd` и `PollCycleDuration` пишут длительность цикла в гистограмму | запись |
| `internal/infra/cibridge/bridge.go:915` | вызов `PollCycleEnd` в цикле обновления CI | подключение |
| `internal/infra/observe/observe.go:540-544` | `MergeDuration` пишет длительность мержа | запись |
| `internal/app/orchestrator/orchestrator.go:485`, `internal/infra/cibridge/verdict_applier.go:411` | точки вызова `MergeDuration` | подключение |
| `internal/infra/observe/observe.go:652-655` | `WakeDuration` пишет длительность доставки | запись |
| `internal/infra/observe/observe.go:767-778` | `RunEnd` пишет длительность прохода оркестратора | запись |
| `internal/infra/observe/metrics.go:379` | `HistogramNames` и JSON-экспорт гистограмм | экспорт |
| `cmd/beacon/mcp.go:2188-2190` | сохраняет снимок метрик в `runtime_stats` после каждого цикла | сохранение |
| `cmd/beacon/inspect.go:63,194-198` | читает `runtime_stats` в поле `runtimeStats` | чтение |
| `internal/infra/server/metrics.go:28-45` | отдельный набор: `call_total`, `call_duration_seconds`, `rss_bytes`, gauge поллера | отдельная метрика |
| `docs/evals/e11-metrics-counters.md` | цель RSS 20-30 MB и проверка gauge поллера | цель, не замер |
| `docs/perf/README.md` | пороги SLO и пустая таблица результатов | контракт замера |

## Фактические замеры

### Замер 1

Команда: `.opencode/bin/beacon inspect --json`.
Время: 2026-11-22T12:35:13Z.
Бинарь: `.opencode/bin/beacon`, mtime 2026-11-22 15:20 по локальному времени. Это бинарь MCP-сервера, который работает в этой среде. Значения пришли из строки `runtime_stats`.

| Гистограмма | count | sum, с | Накопленные бакеты |
| --- | ---: | ---: | --- |
| `poll_cycle_duration_seconds` | 519 | 605.623985756 | ≤1 мс: 154; ≤5 мс: 270; ≤10 мс: 283; ≤50 мс: 292; ≤100 мс: 292; ≤500 мс: 294; ≤1 с: 418; ≤5 с: 476; ≤30 с: 518; ≤300 с: 519 |
| `merge_duration_seconds` | 39 | 308.178452428 | ≤1 с: 0; ≤5 с: 0; ≤30 с: 38; ≤300 с: 39 |
| `wake_duration_seconds` | 1 | 0.095488737 | ≤0.1 с: 1 |
| `run_duration_seconds` | 0 | 0 | все бакеты 0 |

Счётчики из того же снимка: `poll_cycles_total` 519, `merge_attempts_total` 39, `merge_success_total` 39, `merge_failures_total` 0, `active_watches` 2, `actor_errors_total` 77, `pollerActive` true, `pollerPID` 1284861, `outbox_depth` 0, `verdict_published_total` `{"merged":44,"rebase":1}`, `wake_skipped_total` 44.

### Замер 2

Команда: `.opencode/bin/beacon inspect --json | jq '{histograms: .runtimeStats.histograms, poll_cycles_total: .runtimeStats.poll_cycles_total, merge_attempts_total: .runtimeStats.merge_attempts_total, active_watches: .runtimeStats.active_watches, pollerActive: .runtimeStats.pollerActive}'`.
Время: 2026-11-22T12:35:34Z, через 21 с после замера 1.

| Гистограмма | count | sum, с | Прирост за 21 с |
| --- | ---: | ---: | --- |
| `poll_cycle_duration_seconds` | 520 | 605.628146453 | 1 цикл, 0.004160697 с |
| `merge_duration_seconds` | 39 | 308.178452428 | 0 |
| `wake_duration_seconds` | 1 | 0.095488737 | 0 |
| `run_duration_seconds` | 0 | 0 | 0 |

Бакеты `poll_cycle_duration_seconds` в замере 2: ≤1 мс 154, ≤5 мс 271, ≤10 мс 284, ≤50 мс 293, ≤100 мс 293, ≤500 мс 295, ≤1 с 419, ≤5 с 477, ≤30 с 519, ≤300 с 520.

### Что дают гистограммы

Это границы бакетов, не точные перцентили. Гистограмма не хранит отдельные значения, поэтому точнее указать интервал.

- Среднее `poll_cycle_duration_seconds`: 605.628146453 / 520 = 1.165 с.
- Медиана цикла лежит в интервале (1, 5] мс. Из 520 циклов 154 укладываются в 1 мс, 271 в 5 мс.
- p95 цикла лежит в интервале (5, 30] с. Из 520 циклов 477 укладываются в 5 с, 519 в 30 с.
- Хвост цикла: 124 цикла (23.8%) в интервале (0.5, 1] с, 58 циклов (11.2%) в (1, 5] с, 42 цикла (8.1%) в (5, 30] с.
- Среднее `merge_duration_seconds`: 308.178452428 / 39 = 7.90 с. Все 39 мержей больше 5 с. 38 попадают в (5, 30] с, одно в (30, 300] с.
- `run_duration_seconds` не имеет наблюдений при 520 циклах поллера и 39 мержах.

### Числа из существующих документов

Эти числа не сняты в этом исследовании. Они процитированы с указанием источника.

- `docs/perf/README.md`, 2026-11-19, спайк T0.1: VmHWM на 60 горутин равен 14184 КБ в штатном режиме и 16032 КБ в длинном прогоне.
- `docs/perf/README.md`, 2026-11-19, спайк T0.2: `rateLimit.cost` равен 1 для tier-1 и tier-2 на 15, 50, 60 и 100 алиасах.
- `docs/perf/README.md`, отчёт `M-go-test-speed.md`: 12 прогонов CI занимают от 47 до 64 с, медиана около 57 с. Причина в сломанном кэше сборки и ожидании файлового замка в тестах.

### Что не удалось измерить

- cardindex gate. Установленный `.opencode/bin/beacon-gates` содержит только `capabilities`, `layers-ast` и `layers-list`. Проверка `tools/beacon-gates/cardindex/check.go` запускается через `go run`. Условие задачи запрещает локальные гейты, поэтому время её работы не записано.
- `hyperfine` в среде не установлен.
- `beacon index build` пересобирает индекс в state dir, поэтому я его не запускал.

## Таблица разрывов

| Метрика | Есть ли в коде | Фактический замер | Разрыв |
| --- | --- | --- | --- |
| Латентность цикла поллера | Да. `observe.go:230,448,455`, вызов `cibridge/bridge.go:915` | count 520, sum 605.628 с, среднее 1.165 с, медиана в (1, 5] мс, p95 в (5, 30] с | Нет записи перцентилей в `docs/perf`. Нет порога. Бакеты 5/30/300 с слишком крупные, чтобы найти причину хвоста. Нет разбивки по триггеру цикла |
| Длительность мержа | Да. `observe.go:231,542`, вызовы `orchestrator.go:485` и `verdict_applier.go:411` | count 39, sum 308.178 с, среднее 7.90 с, все больше 5 с | Нет записи перцентилей и порога в `docs/perf`. Все значения попадают в один широкий бакет (5, 30] с, поэтому рост на 2 с не виден |
| Ожидание блокировки БД | Нет гистограммы. `store/db.go:223` задаёт `busy_timeout(5000)`, `store/db.go:264` блокирует мьютекс writer | Нет | Неизвестно, сколько из 1.165 с среднего цикла приходится на ожидание записи. Таймаут 5 с не виден в метриках |
| Латентность batch GraphQL | Нет. `github/batch_provider.go` не вызывает `observe`, только часы счётчика в строке 129 | Нет | Нет гистограммы длительности батча, нет p95, нет связи задержки с размером чанка |
| Длительность GC worktree | Нет таймера. CLI в `cmd/beacon/gc.go:760`, порт демона в `cmd/beacon/mcp.go:1951` и `cibridge/bridge.go:265` | Нет | Нельзя оценить вклад GC в цикл демона и выбрать интервал. Нет разницы между dry-run и `--apply` |
| Время пересборки индекса карточек | Нет таймера. `taskstore/index.go:208`, гейт `tools/beacon-gates/cardindex/check.go:89` | Нет | Комментарий в `index.go` ссылается на `docs/research/sqlite-perf-audit.md`, которого нет в origin/main. Нет числа для 931 карточки |
| Длительность прохода оркестратора | Да. `observe.go:233,778`, вызов `orchestrator.go:380` | count 0 при 520 циклах поллера | Гистограмма определена и подключена, но не получает наблюдений в рантайме MCP, который пишет `runtime_stats` |

## Карточки

| ID | Тема | Проверка |
| --- | --- | --- |
| PF-01 | Гистограмма ожидания блокировки SQLite | Тест с двумя writers даёт count ≥ 1 и sum > 0 в новой гистограмме |
| PF-02 | Гистограмма латентности batch GraphQL | Тест на фикстуре даёт count ≥ 1 и sum, близкое к затраченному времени |
| PF-03 | Длительность GC worktree | Команда и порт демона пишут длительность, поле больше нуля |
| PF-04 | Длительность пересборки индекса карточек | `RebuildDir` возвращает длительность, замер с числом карточек лежит в docs |
| PF-05 | Перцентили поллера и мержа в SLO-таблице | В `docs/perf/README.md` есть строки с датой, командой и границами перцентилей |
| PF-06 | Наблюдения `run_duration_seconds` в рантайме MCP | После одного цикла демона `beacon inspect --json` показывает count ≥ 1 |

## Источники

- `internal/infra/observe/observe.go`, `internal/infra/observe/metrics.go`
- `internal/infra/server/metrics.go`
- `internal/infra/cibridge/bridge.go`, `internal/infra/cibridge/verdict_applier.go`
- `internal/app/orchestrator/orchestrator.go`
- `internal/infra/store/db.go`, `internal/infra/taskstore/index.go`
- `internal/infra/github/batch_provider.go`
- `cmd/beacon/inspect.go`, `cmd/beacon/gc.go`, `cmd/beacon/mcp.go`
- `docs/evals/e11-metrics-counters.md`, `docs/perf/README.md`
- `tools/beacon-gates/cardindex/check.go`

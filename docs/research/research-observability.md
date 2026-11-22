# Исследование наблюдаемости и метрик

## Способ проверки

Отчёт составлен на коммите `8608917dab33456083ef9f510e8b883e1309a02e` ветки `origin/main`. Файлы читались в worktree `worktrees/obs-research`. Тесты, сборка и gate локально не запускались. Все выводы получены чтением кода и точечным поиском `rg` по явным каталогам.

Прочитанные источники, чтобы не дублировать бэклог: `docs/research/metrics-audit.md` из общего дерева (коммит `d3b1bfb1`, в `origin/main` этого файла нет), карточки `BCX-75`, `BCX-79`, `AUD-12` из `origin/main`, `docs/evals/e11-metrics-counters.md`, `docs/runbooks/observability.md`. Карточек `AUD-11` и `MEM-08` нет ни в общем дереве, ни в `origin/main`, поиск по `docs` дал 0 совпадений, поэтому сравнение с ними провести нельзя.

Слои взяты из `tools/beacon-gates/gate/config/layers.json`. Все OwnerFiles карточек существуют в `origin/main`. Внутри одной фазы файлы не пересекаются.

## Границы относительно существующего бэклога

`metrics-audit.md` разбирает счётчики `observe.Metrics`: `wake_duplicate_total`, `fix_total`, `active_watches`, `outbox_depth`, `fixers_spawned_total`, пустые метки, снимок `runtime_stats`, `blocks_total`, `verdict_published_total` и не подключённый `call_total`. Эти темы сюда не входят.

`BCX-79` описывает расхождение `grpc` в `otel.go` и `NewOTLPExporter`. Находка 2 ниже говорит только о том, что предупреждение об этом расхождении не попадает в журнал. `BCX-75` требует описать переменные окружения. Находка 7 требует проверки конфигурации в `beacon doctor`, а не документации. `AUD-12` требует убрать бесконечный инкремент `file_adapter_reads_total`. Находка 3 говорит о стоимости чтения всего журнала в `beacon_stats`.

## Находки

### 1. OTLP-экспортер теряет спаны до заполнения батча. Приоритет P1

`internal/infra/observe/otel.go:54` задаёт `defaultBatchSize = 64`. `OTLPExporter.Emit` (`otel.go:445`) добавляет спан в `e.batch` и вызывает `Flush` только при `len(e.batch) >= e.batchSize` (`otel.go:452`, `:455`). Других точек отправки, кроме `Flush` (`otel.go:463`) и `Shutdown` (`otel.go:497`), в пакете нет. Поиск `time.NewTicker` и `time.Tick` в `internal/infra/observe` дал 0 совпадений. Долгоживущий демон, который за минуту выдал меньше 64 спанов, не отправляет их в коллектор до завершения процесса. Дополнительно `Emit` вызывает `Flush` синхронно в горутине вызывающего, а `NewOTLPExporter` ставит HTTP-таймаут `defaultOTLPTimeout = 2s` (`otel.go:55`, `:419`). Медленный коллектор задерживает вызов, который создал спан, до двух секунд.

OwnerFiles: `internal/infra/observe/otel.go`, слой `internal/infra/observe`, ранг 0.

### 2. Предупреждения о конфигурации OTEL пишутся до установки журнала. Приоритет P2

`WarnConfig` (`internal/infra/observe/otel.go:170-176`) использует стандартный `log.Print`. `ConfigFromEnv` вызывает его при неизвестном экспортере (`otel.go:189-191`) и неизвестном протоколе (`otel.go:192-197`). Функция вызывается из `init()` пакета `observe` (`otel.go:716-718`), то есть до `main`. `setupObserve` (`cmd/beacon/observe.go:220-236`) перенаправляет `log.SetOutput` только на строке 236, а `main` вызывает его в `cmd/beacon/main.go:130`. Предупреждение уходит в stderr до того, как появится файловый writer. Для `mcp` и `daemon` комментарий в `cmd/beacon/observe.go:229-230` прямо говорит, что демон ничего не пишет в stderr. Оператор не находит причину тихого local fallback в `beacon logs`.

OwnerFiles: `internal/infra/observe/otel.go`, `cmd/beacon/observe.go`.

### 3. `beacon_stats` перечитывает весь журнал событий. Приоритет P1

`PersistStats` (`internal/infra/store/observability.go:463-473`) вызывает `LoadEvents` (`:417-457`) только для того, чтобы получить список `loadErrors`. `LoadEvents` делает `SELECT ... FROM events ORDER BY seq` и для каждой строки выполняет `json.Unmarshal` payload (`:442`). Стоимость чтения растёт вместе с журналом, хотя_stats нужен только счётчик повреждённых строк. `beacon_stats` это основной инструмент диагностики, его вызывают агенты и демон по правилам `AGENTS.md`. Рост стоимости виден на длинной истории вотчей.

OwnerFiles: `internal/infra/store/observability.go`, слой `internal/infra`, ранг 1.

### 4. Снимок SharedGate доступен только тестам. Приоритет P2

`SharedGate.ActiveWaiters` (`internal/infra/store/observability.go:580-590`) комментируется как способ сделать гейт наблюдаемым, но все вызовы находятся в `internal/infra/store/observability_test.go`. Единственный продакшн-сигнал это WARNING в `SharedGate.Wait` (`:555-557`), который печатается один раз на раунд при втором ожидающем. Число объединённых ожиданий не попадает ни в `beacon_stats`, ни в метрики.

OwnerFiles: `internal/infra/store/observability.go`.

### 5. `--tail` не ограничивает память при чтении логов. Приоритет P1

`ReadLogs` (`internal/infra/observe/query.go:30-112`) накапливает все совпавшие строки в срез `matched` (`:71`, `:86`, `:88`), а затем применяет `Tail` (`:101-107`). Флаг ограничивает только вывод. Команда `beacon logs --tail 40 --errors`, которая рекомендована в `AGENTS.md`, сначала загружает весь набор логов в память. Буфер сканера на файл равен 4 МиБ (`:79`).

OwnerFiles: `internal/infra/observe/query.go`, слой `internal/infra/observe`.

### 6. Фильтры логов отбрасывают текстовые строки. Приоритет P2

`matchLogLine` (`internal/infra/observe/query.go:123-130`) при любом фильтре вызывает `parseLogRecord` и возвращает false, если строка не JSON. Функции `encodeLogRecordJSON` (`:171-181`) и `parseLogFmtLine` (`:187-240`) умеют разбирать текстовый формат slog, но вызываются только после прохождения фильтра. Поэтому `beacon logs --errors` не находит строк с `level=ERROR` в текстовом формате и строк `log.Print`. Незаданный `BEACON_LOG_FORMAT` включает текстовый формат в `NewLogger` (`internal/infra/observe/log.go:35-40`).

OwnerFiles: `internal/infra/observe/query.go`.

### 7. `beacon doctor` не проверяет наблюдаемость. Приоритет P1

`runDoctor` (`cmd/beacon/doctor.go:33-64`) выполняет ровно три проверки: `checkGo`, `checkGates`, `checkStateDir` (`:52`). Нет проверки переменных `BEACON_OTEL_*`, порта `BEACON_METRICS_PORT`, каталога логов и доступности OTLP-назначения. Результат печатается простым `fmt.Fprintf` в `c.out` (`:55`), флага `--format json` у команды нет. Тихий local fallback из находки 2 не становится видимым при стандартной проверке здоровья.

OwnerFiles: `cmd/beacon/doctor.go`, слой `cmd`, ранг 3.

### 8. Метрики поллера не доходят до `/metrics`. Приоритет P2

`publishPollerStatus` (`cmd/beacon/mcp.go:1566-1570`) пишет состояние в `pollerMetrics`, где `pollerMetrics = server.NewMetrics()` (`cmd/beacon/mcp.go:1562`). `SetPoller` (`internal/infra/server/metrics.go:241`) и экспорт gauges `beacon_poller_active` и `beacon_poller_pid` (`metrics.go:531-536`) работают на этом экземпляре. Эндпоинт `/metrics` поднимается через `metricsServerStart` (`cmd/beacon/observe.go:86-92`) с экземпляром `observe.Metrics`, созданным в `setupObserve` (`cmd/beacon/observe.go:234`). Это другой объект. Состояние поллера доходит только до `runtime_stats` через `withPollerStats` (`cmd/beacon/mcp.go:1576-1596`), который читает `beacon inspect`. В `/metrics` серий `beacon_poller_*` нет.

OwnerFiles: `cmd/beacon/mcp.go`, `internal/infra/server/metrics.go`, `internal/infra/observe/metrics.go`, `cmd/beacon/observe.go`. Фаза P3, чтобы не пересекаться с находкой 2.

### 9. Ошибки доставки теряют адаптер в CLI-обёртке. Приоритет P1

`observingDeliveryTarget.Prompt` (`cmd/beacon/observe.go:294-309`) при ошибке вызывает `observe.WakeFailed(t.adapter, sessionID, err)` (`:297`, `:302`), а при успехе подставляет `opencode`, если `t.adapter == ""` (`:305-309`). Для одного и того же targets метрика ошибки получает `adapter=""`, а метрика успеха `adapter="opencode"`. Сигнатура `WakeFailed(adapter, watchID string, err error, ...)` находится в `internal/infra/observe/observe.go:686`. Вторым аргументом обе функции получают `sessionID`, хотя поле называется watchID (`observe.go:599`). В логе ошибки и успехи одного адаптера разносятся по разным сериям.

OwnerFiles: `cmd/beacon/observe.go`, слой `cmd`.

## Топ-3

1. Находка 1: трассировка демона не попадает в коллектор без 64 спанов или вызова `Shutdown`.
2. Находка 3: каждое чтение `beacon_stats` разбирает весь журнал событий.
3. Находка 9: ошибки доставки из CLI получают пустую метку адаптера.

## Вне зоны

Код не менялся. Карточки цикла Beacon создаются отдельным batch-файлом, применение batch требует команды владельца.

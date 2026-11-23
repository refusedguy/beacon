# Аудит конфигурации и переменных окружения

Дата: 2026-11-21. Репозиторий: beacon-impl. Режим: read-only, код не менялся, сборки и тесты не запускались.

## Метод и границы

Источник фактов: `rg` с явными каталогами `cmd internal tools` по шаблонам `BEACON_`, `os.Getenv`, `os.LookupEnv`, `envconfig`, `t.Setenv`, плюс чтение самих файлов. Для сверки с документацией отдельно просмотрены `README.md` и `docs/**/*.md`. Дополнительно зафиксированы vars вне declared scope (`evals`, `.opencode`, `tests`, `scripts`) в приложении Б.

`os.LookupEnv` и `envconfig` в `cmd internal tools` не встречаются. Весь конфиг читается через `os.Getenv`, часто через передаваемую в функцию `lookup func(string) string` на границе пакета.

Тема частично пересекается с `docs/research/security-audit.md`. Здесь повторно разобраны только env-аспекты, с ссылками на находки того аудита. Отдельно проверены ntfy, actor-проверки и таймауты, как запрошено.

Заголовки таблиц: «источник» это файл и строка чтения или объявления; «валидация» это то что код реально делает с значением; «документация» это файлы где переменная названа; «риск» это практическое последствие ошибки конфига, не severity уязвимости.

## Полная инвентаризация

### A1. Пути, состояние, логи, наблюдаемость

| env | источник | default | валидация | документация | риск |
| --- | --- | --- | --- | --- | --- |
| `BEACON_STATE_DIR` | `cmd/beacon/doctor.go:244,262` | пусто, тогда `<XDG_STATE_HOME>/beacon/projects/<name>-<hash>` при `.git` в cwd, иначе `<XDG_STATE_HOME>/beacon` | нет, любая строка становится путём без проверки существования | README.md:45, runbooks/observability.md:16,21,130, runbooks/verification.md:23, ещё 11 файлов | низкий: операторский параметр, но опечатка даёт новый пустой store |
| `XDG_STATE_HOME` | `cmd/beacon/attach.go:56`, `agents.go:132`, `doctor.go:299`, `internal/infra/opencode/client.go:58`, `taskstore/store.go:314`, `internal/infra/server/config.go:51` | `~/.local/state` | нет; относительный путь остаётся относительным (`server/config.go:47`) | runbooks/observability.md, runbooks/verification.md, ADR 0086 и др. | низкий |
| `BEACON_PROJECT_ROOT` | `cmd/beacon/doctor.go:249,269`, `cmd/beacon/mcp.go:1143` | `os.Getwd()` | `TrimSpace`, нет проверки что каталог существует | tasks/BS.79.md, T3.363.md, T3.374.md, runbooks/verification.md:23, process-leak-rootcause.md | средний: плагин передаёт root, неверное значение меняет ключ проекта и store |
| `BEACON_CONFIG` | `internal/app/orchestrator/checkall.go:70,152`, `internal/infra/mcp/tool_watch_pr.go:49,99` | `~/.config/beacon/config.yaml` | `TrimSpace`; отсутствующий файл не ошибка | ADR 0259, 0148, 0181, 0216, tasks/T3.365.md, evals/w21-inheritance.md | низкий: опечатка молча даёт дефолтную политику |
| `BEACON_LOG_FORMAT` | `cmd/beacon/observe.go:75,208` | text | `NewLogger` (`observe/log.go:40`) сравнивает с `json` без регистра, всё остальное даёт text без предупреждения | runbooks/observability.md:39, runbooks/verification.md:22, cli-ux-audit.md:73 | низкий: опечатка даёт logfmt вместо JSON, запрос JSON-лога молча лжёт |
| `BEACON_LOG_LEVEL` | `cmd/beacon/observe.go:78,203`, `observe/log.go:33,230` | info | `ParseLevel` (`observe/observe.go:29`) разбирает debug/info/warn/error; CLI пишет warning в stderr при неверном значении, `OpenServerLog` молча берёт info | runbooks/observability.md:44, verification.md:22 | низкий, но поведение различается между CLI и server log |
| `BEACON_METRICS_PORT` | `cmd/beacon/observe.go:99`, `observe/metrics.go:496,512` | пусто, endpoint не открывается | `strconv.Atoi`, bind только на `127.0.0.1`; ошибка пишется в stderr и не роняет команду | нет | низкий: /metrics просто не поднимется |
| `BEACON_OTEL_ENABLED` | `observe/otel.go:29,157` | false | `strings.EqualFold(TrimSpace(v),"true")`, только `true` включает; `1` выключен | ADR 0185, tasks/T3.393.md, evals/otel-noop-by-default.md | низкий; комментарий в коде говорит «литерал true», но `TRUE` тоже работает |
| `BEACON_OTEL_EXPORTER` | `observe/otel.go:32,158` | пусто, local slog sink | `TrimSpace`, любые значения молча дают local | ADR 0185, tasks/T3.393.md | низкий: опечатка `otlp` отключает экспорт без ошибки |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `observe/otel.go:35,152` | пусто, local spans | `url.Parse`, требуется scheme и host; схема не ограничена http/https, хост не ограничен (`otel.go:360-363`) | security-audit.md находка 10, ADR 0192, tasks/BS.47.md | средний: при включённом OTLP исходящий POST на любой адрес |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | `observe/otel.go:37,153` | пусто | та же проверка, перекрывает базовый endpoint | security-audit.md находка 10 | средний: тот же канал, более прямой адрес |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `observe/otel.go:40,160` | `http/protobuf` | `normalizeProtocol`: `http/json`/`json`, `grpc`, всё остальное в `http/protobuf`; `grpc` затем отвергается `NewOTLPExporter` (`otel.go:367-369`) и даёт local fallback | tasks/T3.393.md, комментарий в `otel.go:38-39` | средний: documented valid value `grpc` на практике отключает OTLP |

### A2. Каналы, уведомления, GitHub

| env | источник | default | валидация | документация | риск |
| --- | --- | --- | --- | --- | --- |
| `BEACON_CHANNELS` | `cmd/beacon/server.go:33,57,79-86` | false | `1/true/yes/on` без регистра, всё остальное false без предупреждения | только security-audit.md находка 1 | высокий: выключенный ingress не защищает слэш-команды (`up.go:275-276`) |
| `BEACON_CHANNEL_SENDERS` | `cmd/beacon/server.go:37,60-75` | пусто, ни одного отправителя | строгая: `sender=actor`, actor через `domain.NewActorID` (запрещены пробелы и control chars, лимит длины); неверная запись это exit 2 в `serverRunner` (`server.go:135-139`) и `runUp` (`up.go:137-141`) | нет | высокий: реестр не задокументирован, а он единственный источник actor для ingress |
| `BEACON_TELEGRAM_TOKEN` | `wake/telegram.go:22,55` | пусто, sink не настроен | `TrimSpace`; формат токена не проверяется | нет | средний: секрет в env без документации; при `BEACON_TELEGRAM_API` уходит в URL path |
| `BEACON_TELEGRAM_CHAT` | `wake/telegram.go:23,56` | пусто | `TrimSpace` | нет | низкий |
| `BEACON_TELEGRAM_API` | `wake/telegram.go:24,57` | `https://api.telegram.org` (`telegram.go:26,74-75`) | `TrimSpace`, `TrimRight("/")`; схема и хост не проверяются | security-audit.md находка 3 | высокий: токен в пути запроса (`telegram.go:167`) уходит на произвольный хост |
| `BEACON_GITHUB_TOKEN` | `github/rest.go:38,47`, `mcp/tool_get_result.go:569` | пусто, затем `GITHUB_TOKEN`, затем `gh auth token` | `TrimSpace`; пустое значение молча пропускается | tasks/T2.10.md, T0.2, T3.360.md, security-audit.md | высокий: единственный токен для basic read path, но failure excerpt читает только его |
| `GITHUB_TOKEN` | `github/rest.go:38,47` | пусто | `TrimSpace` | tasks/T2.10.md, security-audit.md | высокий: fallback injected в CI, failure excerpt его игнорирует |
| `BEACON_GITHUB_API` | `mcp/tool_get_result.go:573-580` | `https://api.github.com` | `TrimSpace`, `TrimRight("/")`; без проверки схемы, хоста и пути | security-audit.md находка 2 | высокий: Bearer-токен уходит на произвольный хост; при этом переменная не влияет на основной GitHub-клиент |

### A3. HTTP, webhook, ACP, таймауты

| env | источник | default | валидация | документация | риск |
| --- | --- | --- | --- | --- | --- |
| `BEACON_HTTP_ADDR` | `cmd/beacon/mcp.go:752`, `httpapi/server.go:35,489,574-583` | `127.0.0.1:0` | `net.SplitHostPort`, `Atoi(port)`, хост принудительно `127.0.0.1` | нет | низкий по сети, средний по документу: опечатка роняет старт HTTP API |
| `BEACON_WEBHOOK_ADDR` | `cmd/beacon/mcp.go:803`, `webhook/receiver.go:28` | `127.0.0.1:8788` | хост принудительно loopback, порт числовой; ошибка старта optional receiver не роняет MCP | нет | низкий |
| `BEACON_GATES` | `cmd/beacon/doctor.go:221-228` | пусто, тогда `PATH`, затем `tools/beacon-gates/beacon-gates` | `os.Stat`, файл обязан существовать и не быть каталогом | нет | низкий: только `doctor`, в проде не читается |
| `BEACON_SESSION_ID` | `cmd/beacon/orchestrator.go:25,108`, `daemon.go:219` | пусто | `TrimSpace`; формат `ses_` не проверяется | README.md:197 | средний: неверный ID меняет привязку оркестратора |
| `BEACON_PARENT_SESSION` | `cmd/beacon/spawn.go:119,164`, `prcreate.go:35,306` | пусто | `TrimSpace` | нет | средний: неверная сессия адресата доставки или PR-подписчика |
| `BEACON_TASK_ID` | `cmd/beacon/attach.go:274` | пусто | `TrimSpace`, формат карточки не проверяется | нет | низкий |
| `BEACON_TASK_DEADLINE` | `cmd/beacon/attach.go:154-159` | пусто | `time.Parse(RFC3339)`, неверное значение молча игнорируется | нет | средний: дедлайн тихо теряется, запись сохраняется без него |
| `BEACON_AGENT` | `cmd/beacon/task.go:212` | пусто | `TrimSpace`; пустое значение без флага это exit 1 | docs/beacon-task-cli-spec.md:206,209,289 | низкий |
| `BEACON_ROOT` | `cmd/beacon/task.go:700` | `findProjectRoot()` | `TrimSpace` | нет | средний: конфликтует с `BEACON_REPO` в скриптах и `BEACON_PROJECT_ROOT` в плагине |
| `BEACON_PR_PROVIDER` | `prcreate/config.go:11,18` | `github` | `TrimSpace`, enum не проверяется | нет | низкий: неизвестный провайдер упадёт позже в провайдере |
| `BEACON_ACP_RUNNER` | `cmd/beacon/spawn.go:249,257` | `opencode` | `TrimSpace`, исполняемый файл не проверяется | нет | средний |
| `BEACON_ACP_COMMAND` | `cmd/beacon/spawn.go:250,263-265` | пусто, используется runner | `TrimSpace`, команда полностью заменяет runner; проверки нет | security-audit.md находка 8 | высокий: кто управляет env, выбирает исполняемый файл |
| `BEACON_ACP_ARGS` | `cmd/beacon/spawn.go:251,266-268` | пусто | `strings.Fields`, кавычки не интерпретируются | security-audit.md находка 8 | средний: аргументы с пробелами нельзя передать |
| `BEACON_DELIVER_ADAPTER` | `cmd/beacon/deliver.go:318,334` | пусто, остаётся installed target / unsupported | `TrimSpace`, enum здесь не проверяется | evals/w05-pi-rpc-wake.md:15, tasks/T3.217.md:21 | низкий: опечатка молча сохраняет unsupported |
| `BEACON_DELIVER_PI_MODE` | `cmd/beacon/deliver.go:319,335-340` | `queue` | `wake.ParsePiDeliveryMode` (`wake/registry.go:252-260`), только `queue`/`steer`, неверное значение это `cfg.Err` и fallback | evals/w05-pi-rpc-wake.md:15,17, tasks/T3.217.md:21 | низкий |
| `BEACON_DELIVER_PI_TIMEOUT` | `cmd/beacon/deliver.go:320,341-348` | 0, клиентский default | `ParseDuration`, `>0`, неверное значение это `cfg.Err` и fallback | evals/w05-pi-rpc-wake.md:17, tasks/T3.217.md:21 | низкий |
| `BEACON_DAEMON_INTERVAL` | `cmd/beacon/daemon.go:25,219`, `cmd/beacon/mcp.go:60-68` | 3m (`orchestrator/daemon.go:24`) | `ParseDuration`, `>0`, неверное значение молча даёт 3m | tasks/BS.8.md:11, T3.296.md | средний: опечатка меняет цикл демона без предупреждения |
| `BEACON_VERDICT_POLL_INTERVAL` | `cmd/beacon/mcp.go:79,82-86` | 750ms (`mcp.go:74`) | `ParseDuration`, `>0`, неверное значение молча даёт 750ms | нет | средний: без документации, влияет на свежесть `beacon_wait_result` |
| `BEACON_SYNC_CALL_TIMEOUT` | `cmd/beacon/mcp.go:213-224` | 30s, максимум 120s | `ParseDuration`, `>0`, clamp до 120s, неверное значение молча даёт 30s | нет | средний: меняет поведение долгих MCP-вызовов без предупреждения |
| `BEACON_INIT_EVENT` | `cmd/beacon/mcp.go:172` | пусто, precheck не запускается | нет | нет | средний: неверное имя события блокирует старт MCP на 10s |
| `BEACON_INIT_BYPASS` | `cmd/beacon/mcp.go:173-176` | false | точный switch `1/true/yes/on` без `TrimSpace` и `ToLower`; `TRUE` или ` 1` не срабатывают | нет | средний: расхождение с другими булевыми переменными, тихий precheck failure |
| `BEACON_INIT_EVENT_TIMEOUT` | `cmd/beacon/mcp.go:177-181` | 10s (`mcp.go:133`) | `ParseDuration`, `>0`, неверное значение молча даёт 10s | нет | низкий |
| `BEACON_MCP_REMOTE` | `cmd/beacon/mcp.go:381,384-389` | false | `1/true/yes/on` с `ToLower` и `TrimSpace`, всё остальное false | нет | средний: включает другой набор инструментов без документации |

### A4. Переменные только в тестах

| env | источник | Что делает | Документация |
| --- | --- | --- | --- |
| `BEACON_WAKE_TARGET` | `cmd/beacon/checkall_test.go:85,120,158` | Устанавливается в `ses_ignored_env`, но production-код его не читает. Настоящий `wakeTarget` живёт в `Config` из `BEACON_CONFIG` (`checkall.go:80`) | нет |
| `BEACON_CONTRACT_TEST` | `internal/app/orchestrator/checkall_contract_test.go:54-55` | Гейт live GraphQL контрактного теста | нет |
| `BEACON_W10A_ACP_MOCK` | `internal/infra/wake/acp_runtime_test.go:15` | Имя mock env в тесте ACP | нет |
| `GH_TOKEN`, `GITHUB_TOKEN_FILE` | только в `_test.go` | Тестовые токены | нет |

## Расхождения

### Env в коде, но нет в документации

21 production-переменная либо не названа в документации вообще, либо упомянута только в карточках задач и research-отчётах, но не в README и не в runbook:

`BEACON_CHANNEL_SENDERS`, `BEACON_TELEGRAM_TOKEN`, `BEACON_TELEGRAM_CHAT`, `BEACON_ROOT`, `BEACON_PARENT_SESSION`, `BEACON_TASK_ID`, `BEACON_TASK_DEADLINE`, `BEACON_PR_PROVIDER`, `BEACON_METRICS_PORT`, `BEACON_HTTP_ADDR`, `BEACON_WEBHOOK_ADDR`, `BEACON_GATES`, `BEACON_VERDICT_POLL_INTERVAL`, `BEACON_SYNC_CALL_TIMEOUT`, `BEACON_INIT_EVENT`, `BEACON_INIT_BYPASS`, `BEACON_INIT_EVENT_TIMEOUT`, `BEACON_MCP_REMOTE`, `BEACON_ACP_RUNNER`, `BEACON_DAEMON_INTERVAL` (только в задачах), `BEACON_CHANNELS` (только в отчёте по безопасности как уязвимость).

Последствия: оператор не может собрать рабочую конфигурацию по документации. `BEACON_CHANNEL_SENDERS` нужна для того чтобы ingress вообще узнал actor, но её нет ни в README, ни в runbook. `BEACON_DAEMON_INTERVAL` и таймауты MCP описаны только в карточках задач, то есть в разработческом, а не эксплуатационном контексте.

Центральной таблицы env-переменных нет. Сведения разбросаны по README.md, `docs/runbooks/observability.md`, `docs/adrs/*`, `docs/tasks/*`, `docs/evals/*` и research-отчётам. `docs/runbooks/observability.md` описывает только логи и state dir.

### Env в документации, но её не читает код scope

| Имя | Где в документации | Статус |
| --- | --- | --- |
| `BEACON_REPO` | `docs/runbooks/verification.md:16` | Читают только `scripts/verify-beacon.sh:8` и `verify-daemon.sh:8`. В Go-коде нет. Рядом живёт `BEACON_ROOT` (`task.go:700`) и `BEACON_PROJECT_ROOT` (`doctor.go:249`). Три имени для одного понятия root |
| `BEACON_HOME` | `docs/tasks/S5.006.md`, `S7.003.md` | Обе карточки помечены DROPPED, в коде нет |
| `BEACON_WAKE_EVAL` | `docs/research/e2e-coverage-audit.md:45`, `docs/evals/*` | Читает `evals/main.go`, вне scope `cmd internal tools` |
| `BEACON_E2E_INSPECTOR`, `BEACON_MCP_SRC` | `docs/e2e/README.md:44,52,56` | Shell-тесты в `tests/e2e` |
| `BEACON_PERF_WATCHES`, `BEACON_GRAPHQL_URL`, `BEACON_PERF_SEED_USED` | `docs/perf/README.md:67` | Перформанс-скрипты и рантайм, не production Go-код |
| `OTEL_SDK_DISABLED`, `OTEL_NOOP_BY_DEFAULT`, `OTEL_ATTRS_AND_PARENT`, `OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT` | `docs/evals/otel-noop-by-default.md` | Описания eval-сценариев, код их не читает |

### Разные default и semantics в разных местах

1. `BEACON_GITHUB_API` задаёт базу только для failure excerpt в `mcp/tool_get_result.go:573`. Основной GitHub-клиент использует захардкоженные `https://api.github.com` (`cmd/beacon/mcp.go:1106`) и `https://api.github.com/graphql` (`github/graphql.go:27`). Оператор, который указал GitHub Enterprise, получит смешанное поведение: обычный клиент ходит на публичный API, а failure excerpt на его хост.
2. `BEACON_LOG_LEVEL` при неверном значении пишет warning в stderr в `setupObserve` (`observe.go:203-206`), но `OpenServerLog` (`observe/log.go:230-234`) молча берёт info. Одна переменная, два разных поведения.
3. `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` назван valid value в `observe/otel.go:38-39`, но `NewOTLPExporter` (`otel.go:367-369`) отвергает grpc и включает local fallback. Документация обещает экспорт, код его не даёт.
4. Три имени для корня проекта: `BEACON_REPO` (скрипты), `BEACON_ROOT` (Go task CLI), `BEACON_PROJECT_ROOT` (плагин и MCP). У каждого свой default и свой consumer. Единого приоритета нет.
5. `BEACON_OTEL_ENABLED`: комментарий говорит про literal `true`, код использует `EqualFold`, поэтому `TRUE` тоже включает. `BEACON_INIT_BYPASS`, наоборот, не нормализует регистр и пробелы. Bool-переменные разобраны по трём разным правилам: `channelEnabledValue` и `remoteToolsEnabled` нормализуют регистр и пробелы, `BEACON_OTEL_ENABLED` только регистр через `EqualFold`, `BEACON_INIT_BYPASS` не нормализует ничего.

### Отсутствие валидации или тихая замена

Пятнадцать переменных при неверном значении продолжают работать с default без сообщения. Список: `BEACON_LOG_FORMAT`, `BEACON_DAEMON_INTERVAL`, `BEACON_VERDICT_POLL_INTERVAL`, `BEACON_SYNC_CALL_TIMEOUT`, `BEACON_INIT_EVENT_TIMEOUT`, `BEACON_TASK_DEADLINE`, `BEACON_OTEL_EXPORTER`, `BEACON_ACP_RUNNER`, `BEACON_DELIVER_ADAPTER`, `BEACON_PR_PROVIDER`, `BEACON_CHANNELS`, `BEACON_MCP_REMOTE`, `BEACON_PARENT_SESSION`, `BEACON_SESSION_ID`, `OTEL_EXPORTER_OTLP_PROTOCOL`. Для таймаутов это осознанный выбор: bad setting не должен останавливать цикл. Для `BEACON_TASK_DEADLINE` потеря дедлайна тихо меняет данные задачи.

### Проверки по запрошенным темам

`BEACON_GITHUB_API`. Читается в одном месте, `mcp/tool_get_result.go:573`. Default `https://api.github.com`. Scheme, host и путь не проверяются, `Authorization: Bearer` ставится на каждый запрос (`:644`). Перекрывается с security-audit finding 2, P0. Дополнительный конфигурационный риск: переменная не управляет основным клиентом.

`BEACON_CHANNELS` и `BEACON_CHANNEL_SENDERS`. Читаются в `server.go:52-86`, применяются в `internal/infra/channel/ingress.go:127-137`. Senders разбираются строго, неверная запись это exit 2. Но `up.go:275-276` пропускает слэш-команды мимо ingress, поэтому `BEACON_CHANNELS` не ограничивает write-команды Telegram. Документации по `SENDERS` нет. См. security-audit finding 1, P0.

ntfy. В `cmd`, `internal`, `tools`, `scripts`, `.github` реализация отсутствует. Единственное упоминание это `docs/tasks/T3.283.md`, где сказано что адаптер не написан. Транспорт уведомлений это Telegram и порт `NotificationSink` (`wake/notify.go:35-39`). Env для ntfy искать нечего.

Actor-проверки. `BEACON_CHANNEL_SENDERS` проходит через `domain.NewActorID`, который проверяет только формат строки: непусто, не длиннее лимита, без пробелов и control chars (`identity.go:236-246`). Связи с аутентифицированным принципалом нет. Три уровня проверок в `httpapi/server.go:943-954`, `server/api_wake.go:214-219` и `channel/ingress.go:134-137` описаны в security-audit, finding 7, P2.

ACP command. `BEACON_ACP_RUNNER` default `opencode`; `BEACON_ACP_COMMAND` и `BEACON_ACP_ARGS` полностью заменяют command line (`spawn.go:254-269`). `strings.Fields` не понимает кавычки. Оболочки нет, поэтому это не shell injection, но env определяет исполняемый файл. Security-audit finding 8, P2.

OTLP endpoint. `OTEL_EXPORTER_OTLP_ENDPOINT` и `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` читаются в `observe/otel.go:152-160`. `NewOTLPExporter` проверяет наличие scheme и host, но не ограничивает схему и адрес. POST уходит на `tracesURL()` (`otel.go:390-398`). Без секретов в теле, security-audit finding 10, P3. Конфигурационный акцент: `grpc` из документации не работает.

Таймауты и бюджеты. `BEACON_DAEMON_INTERVAL` 3m, `BEACON_VERDICT_POLL_INTERVAL` 750ms, `BEACON_SYNC_CALL_TIMEOUT` 30s с потолком 120s, `BEACON_INIT_EVENT_TIMEOUT` 10s, `BEACON_CI_POLL_INTERVAL` 30s (`cibridge/bridge.go:31,45-55`), `BEACON_DELIVER_PI_TIMEOUT` без дефолта, `defaultOTLPTimeout` 2s. Бюджет ребазов живёт в конфиге проекта `maxRebases` (default 3, диапазон 1..10, `checkall.go:60-62`), а не в env. Отдельной env-переменной для rebase/wake/fix budget нет. Все таймауты, кроме CI poll, не документированы вне кода и карточек.

## Итог, топ-10 по приоритету

| # | Находка | Источник | Почему важно | Приоритет |
| --- | --- | --- | --- | --- |
| 1 | `BEACON_GITHUB_API` без проверки адреса, Bearer-токен уходит на любой хост; переменная при этом не управляет основным GitHub-клиентом | `mcp/tool_get_result.go:573,644`; `mcp.go:1106`; `github/graphql.go:27` | Утечка секрета и обман ожиданий конфига; дублирует security-audit #2 | P0 |
| 2 | `BEACON_CHANNEL_SENDERS` не документирован, а `BEACON_CHANNELS` не защищает слэш-команды | `server.go:33-86`; `up.go:275-276`; `channel/ingress.go:127-137` | Оператор считает канал закрытым, а write-команды проходят мимо реестра | P0 |
| 3 | `BEACON_TELEGRAM_API` без проверки хоста, токен в URL path | `wake/telegram.go:57,167` | Утечка bot token; security-audit #3 | P1 |
| 4 | `BEACON_ACP_COMMAND` и `BEACON_ACP_ARGS` заменяют runner без проверки, кавычки не работают | `spawn.go:254-269` | Env выбирает исполняемый файл; security-audit #8 | P1 |
| 5 | 21 production-переменная без эксплуатационной документации, включая реестр отправителей, адреса слушателей и таймауты | `README.md`, `docs/runbooks/*` | Невозможно собрать рабочую конфигурацию по документации | P1 |
| 6 | Три имени для корня проекта: `BEACON_REPO`, `BEACON_ROOT`, `BEACON_PROJECT_ROOT` | `scripts/verify-beacon.sh:8`; `task.go:700`; `doctor.go:249` | Разные default и consumer, легко перепутать | P1 |
| 7 | Bool-переменные разобраны по трём правилам, `BEACON_INIT_BYPASS` чувствителен к регистру и пробелам | `server.go:79-86`; `mcp.go:384-389`; `otel.go:157`; `mcp.go:173-176` | `TRUE` или ` 1` даёт тихий precheck failure | P1 |
| 8 | 15 переменных молча подменяют неверное значение default, в том числе `BEACON_TASK_DEADLINE` | `attach.go:154-159`, `mcp.go:60-224`, `otel.go:158` | Ошибка конфига не видна в логах, данные пишутся без дедлайна | P2 |
| 9 | `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` объявлен допустимым, но отвергается экспортером | `otel.go:38-39,367-369` | Документация обещает экспорт, код даёт local fallback | P2 |
| 10 | `BEACON_WAKE_TARGET` живёт только в тестах, production его не читает; `BEACON_GATES` только в `doctor` | `checkall_test.go:85,120,158`; `doctor.go:221` | Ложное чувство что параметр работает; лишний параметр в конфиге демона | P2 |

## Приложение А. Что не проверялось

Код не менялся. Сборка, `go test`, `beacon gates` и локальный запуск бинарника не выполнялись. Выводы основаны на чтении исходников и существующей документации. Для проверки уязвимостей достаточно ссылок на security-audit; здесь не делалась новая оценка эксплуатируемости.

## Приложение Б. Env вне declared scope

Эти имена найдены в `evals`, `.opencode`, `tests`, `scripts`, `.github`. Они не читаются production Go-кодом, но попадают в конфигурацию проекта и часто обсуждаются как beacon-переменные.

`BEACON_SERVER`, `BEACON_EVENT`, `BEACON_PROJECT_ROOT_ENV`, `BEACON_WAKE_WEBHOOKS`, `BEACON_WAKE_EVAL`, `BEACON_WAKE_STATE_DIR`, `BEACON_WAKE_EVENT_URL`, `BEACON_REAL_WATCH_ID`, `BEACON_REAL_REVISION`, `BEACON_REAL_OBLIGATION_ID`, `BEACON_EVAL_TMP`, `BEACON_EVAL_PLUGIN`, `BEACON_S2_POLL_MS`, `BEACON_REPO_ROOT`, `BEACON_MCP_SRC`, `BEACON_E2E_INSPECTOR`, `GH_CALLS`, `GITHUB_TOKEN_FILE`, `GITHUB_PATH`.

Их присутствие без единого реестра усиливает находку 5: разобрать, какая переменная относится к рантайму, а какая к харнессу, по текущей документации нельзя.

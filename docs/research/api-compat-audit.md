# Аудит стабильности публичного контракта

Дата: 2026-11-21. Репозиторий: `beacon-impl`, коммит `d3b1bfb`.

## Как снимался замер

Поиск шёл через `rg` с явными каталогами `cmd`, `internal`, `docs/api`, `docs/adrs`, `docs/tasks`, `docs/evals`, `tools` и `evals`. Команды `go test`, `go build` и `beacon gates` не запускались. Ни один файл исходного кода не менялся. Все числа и факты ниже взяты из исходников и конфигурации, а не из запуска кода.

## Публичные контракты

### 1. `tools/list` MCP

Источник правды: `internal/infra/mcp/server.go:106-115`, список `ToolNames` из восьми имён: `beacon_watch_pr`, `beacon_wait_result`, `beacon_get_result`, `beacon_list_running`, `beacon_ack`, `beacon_cancel_watch`, `beacon_stats`, `beacon_defer_wiring`. Дескрипторы и схемы собираются в `internal/infra/mcp/schema.go:295-303`. Побайтовый эталон лежит в `internal/infra/mcp/contract_test.go:24-50`: бюджет 16 KiB, точный размер 16095 байт, SHA-256 `8324205f18b1b4c8e90843aeafcad90b8170c8c7ae7b49733a864b6a358f3aab`, закреплённый порядок из восьми имён.

Потребители: агенты через Code Mode (`tools.beacon.<name>` после `search`), оркестратор, eval-наборы, правило `AGENTS.md` в разделе MCP.

Защита: `TestToolsListContract` (`contract_test.go:129-215`) сверяет имена, аннотации, наличие схем и бюджет. `TestToolsListStabilityAcrossConnections` (`:276-310`) сравнивает два независимых соединения побайтово, проверяет размер, хеш и порядок. ADR 0094 (`docs/adrs/0094-seven-tools.md`) фиксирует восемь имён и бюджет 16 KiB.

Что ломается при изменении: девятый инструмент, смена описания, переименование параметра, смена аннотации или перестановка инструментов меняют хеш и роняют контрактный тест. Обновление эталона без решения владельца делает защиту фиктивной: тест начинает подтверждать новое состояние вместо прежнего.

### 2. Схемы вывода MCP

Источник правды: Go-типы в `internal/infra/mcp/schema.go:24-284`, реестр `outputSchemaFactories` в `:295-303`. Схемы выводятся рефлексией, а не пишутся вручную (ADR 0003, ADR 0009).

Потребители: агенты, которые разбирают ответы `beacon_get_result` и `beacon_wait_result`, включая поля `pollAfterMs`, `pollIntervalMs` и `nextAction` (`schema.go:83-127`).

Защита: `TestOutputSchemasValidateRealResponses` (`contract_test.go:417-520`) прогоняет реальные ответы всех восьми инструментов через объявленные схемы и добавляет поле, которого нет в схеме `get_result`, чтобы проверить совместимость вперёд (`permissiveSchema`, `:351-415`).

Что ломается: удаление или переименование поля ломает разбор у агента. Добавление поля не ломает схему, потому что 구조ные объекты открыты, но меняет SHA `tools/list`. Это две разные защиты для одного изменения.

### 3. `POST /api/v1/human/decide`, контракт `beacon.human-decide.v1`

Источник правды: константа `HumanDecideContract` в `internal/infra/httpapi/server.go:60-63`, структуры `Decision` (`:201-208`) и `DecisionResult` (`:212-220`), таблица `validCommands` (`:927-931`), хендлер `handleDecide` (`:933-991`). Маршрут регистрируется в `:666`.

Потребители: TUI через `internal/tui/client.go:25-27,214-230`, CLI `beacon human` через `httpapi.ProjectionClient` (`cmd/beacon/human.go:61`), внешние приложения, которые читают адрес и токен из `connection.json`.

Защита: `TestContractDecideVersioned` (`internal/infra/httpapi/contract_test.go:414-454`) проверяет эхо версии, отклонение `v9` с `UNSUPPORTED_CONTRACT` и отсутствие вызова sink при отклонении. `client_test.go:157-233` проверяет форму запроса и квитанции. Ответы валидируются против `docs/api/openapi.yaml` через `loadOpenAPI` (`contract_test.go:29,217,260`).

Что ломается: смена имени версии или поля `contract` ломает всех клиентов. Хендлер сознательно принимает запрос без поля `contract`, поэтому старые клиенты продолжают работать (`server.go:951-954`).

### 4. `docs/api/openapi.yaml`

Источник правды: сам файл, OpenAPI 3.1.0, четыре пути: `/api/v1/health`, `/api/v1/snapshot`, `/api/v1/events`, `/api/v1/human/decide` (`docs/api/openapi.yaml:27-204`).

Потребители: внешний UI, для которого контракт и создан (`:5-11`), тесты `internal/infra/httpapi/contract_test.go`.

Защита: `loadOpenAPI` читает файл, `validateAllExamples` (`:279`) проверяет все примеры, `validateBody` (`:217`) сверяет ответы хендлеров с схемами `HealthResponse`, `SnapshotResponse`, `DecisionResult`, `Event` и `Error` (`:331-476`). Проверки нет только на полноту списка путей.

Что ломается: добавление нового маршрута в `server.go` не вызовет ни одного теста, если схема старых ответов не изменилась. Удаление документа не остановит сборку: тест `loadOpenAPI` упадёт, но только при запуске этого пакета.

### 5. Каталог команд `beacon list --format json`

Источник правды: срез `catalog` в `cmd/beacon/catalog.go:20-36` и запись `spawnCatalogEntry`, добавленная в `init()` в `cmd/beacon/main.go:55-67`. Поля:`name`, `summary`, `usage` (`catalog.go:11-16`).

Потребители: агенты, для которых `AGENTS.md` и `main.go:52` прямо указывают эту команду как каталог. Команда `beacon what` читает тот же срез (`catalog.go:82-107`).

Защита: `TestT06ListJSONCatalog` (`cmd/beacon/main_test.go:74-84`) проверяет только, что stdout является JSON. Набор полей, порядок и полнота списка не зафиксированы.

Что ломается: удаление команды из каталога или добавление команды в `usageText` без каталога не ловится тестом. Агент видит неполный список.

### 6. JSON карточек `beacon task list --format json`

Источник правды: `docs/beacon-task-cli-spec.md:64-101` задаёт ровно десять полей и стабильный порядок объектов. Код: `jsonCard` в `internal/app/taskcli/service.go:50-60`, сериализация в `:118-137`, проверка формата в `:77`.

Потребители: агенты и оркестратор, которые читают карточки как машиночитаемый список.

Защита: `cmd/beacon/task_test.go:41-60` проверяет, что вывод разбирается как JSON и что число записей совпадает с числом файлов. Порядок полей, их имена и сортировка объектов эталоном не защищены.

Что ломается: переименование поля `ownerFiles` или смена сортировки ломает потребителей, но тесты останутся зелёными.

### 7. CLI-флаги и строка usage

Источник правды: `usageText` в `cmd/beacon/main.go:20-53` и отдельные `flag.String` в хендлерах, например `cmd/beacon/watch.go:220`, `cmd/beacon/checkall.go:33`, `cmd/beacon/deferred.go:128`, `cmd/beacon/workload.go:125`.

Потребители: люди и агенты, которые запускают команды по тексту `beacon what` и README.

Защита: `main_test.go:616-625` сверяет несколько строк usage и отказ от неизвестного формата. Полного теста соответствия `usageText` и фактических хендлеров нет.

Что ломается: флаг, описанный в README, но отсутствующий в коде, возвращает код 2. Команда в `usageText`, отсутствующая в каталоге, не видна через `beacon list --format json`.

### 8. Второй HTTP-поверх `/api/v1` в `internal/infra/server`

Источник правды: константы путей в `internal/infra/server/api_watches.go:22`, `api_wake.go:22-26`, `api_audit.go:23`, `internal/infra/server/ws.go`, `internal/infra/server/mcp.go:14`. Хендлеры поставляют `Handler()` методы, например `api_watches.go:116` и `api_audit.go:169`.

Потребители: в композиции корня нет ни одного вызова, который монтировал бы эти обработчики. `cmd/beacon/server.go:15,21` импортирует пакет только ради `server.NewManager()` (аренда liveness). Активный сервер поднимается через `httpapi.Start` в `cmd/beacon/mcp.go:751`.

Защита: юнит-тесты самих обработчиков, например `api_watches_test.go:149` и `api_audit_test.go:204`. Ни один тест не проверяет, что маршрут доступен через реальный сервер.

Что ломается: пока surface не смонтирован, изменение путей ни на кого не влияет. Если его случайно подключить, появится второй `/api/v1/audit` и `/api/v1/events` с другой формой ответа, не описанной в `openapi.yaml`.

### 9. ADR как фиксатор контракта

Источник правды: `docs/adrs/0094-seven-tools.md`, `0263-mcp-boundary.md`, `0186-mcp.md`, `0156-orchestrator-workload.md`, `0155-blocked-watch.md`, `0094-seven-tools.md:22-28`, `docs/tasks/RS-MCP-01.md:11`, `docs/tasks/T5.36.md:19`.

Потребители: агенты и разработчики, которые решают, можно ли добавить инструмент или поле.

Защита: только `internal/infra/mcp/contract_test.go`. Ни один гейт не сверяет текст ADR с кодом.

Что ломается: агент читает ADR 0263 и решает, что восьмого инструмента быть не должно, хотя контрактный тест требует восемь. Расхождение стоит на пути любой правки MCP.

## Расхождения документации и кода

### 1. Семь или восемь инструментов

Код и тест фиксируют восемь имён (`server.go:106-115`, `contract_test.go:55-64`). `AGENTS.md` в разделе MCP тоже перечисляет восемь. Ниже перечислены источники, которые всё ещё говорят про семь:

- `README.md:11,22,43,50-62`: «ровно семь инструментов», заголовок «Семь инструментов MCP», таблица на семь строк и фраза «Восьмой инструмент не добавляется без смены контракта».
- `docs/adrs/0263-mcp-boundary.md:1,7,11,15`: заголовок «Граница MCP ровно семь инструментов», решение на семь имён, прямое требование отдельного ADR для восьмого. ADR 0094 уже принял восьмой инструмент.
- `docs/adrs/0186-mcp.md:7`: семь инструментов и бюджет 64 KiB вместо 16 KiB.
- `docs/adrs/0156-orchestrator-workload.md:11,21,31` и `0155-blocked-watch.md:9,15`: семь инструментов и бюджет 64 KiB.
- `evals/main.go:42-43`: `contractToolCount = 7`. Eval сверяет каталог с семью именами, хотя сервер отдаёт восемь.
- `docs/runbooks/verification.md:41`: `tools/list` вернул ровно семь инструментов.
- `docs/evals/e02-seven-tool-contract.md`, `docs/evals/r10-seven-tools.md`, `docs/evals/e04-executor-state-visible-through-seven-tools.md`: сценарии на семь инструментов.

Это главный дрейф в репозитории. Источник правды один, а описаний с другим числом больше десяти.

### 2. Порядок инструментов: ADR против теста

ADR 0094 (`docs/adrs/0094-seven-tools.md:22`) и ADR 0263 (`:11`) говорят, что порядок имён частью контракта не является. `contract_test.go:37-50` вводит `toolsListOrder` и комментарий «Pinning the order makes that behavior part of the contract». `TestToolsListStabilityAcrossConnections` (`:307-309`) сверяет порядок построчно. Тест прав, документы устарели.

### 3. `openapi.yaml` описывает четыре из семи маршрутов активного сервера

`internal/infra/httpapi/server.go:660-666` регистрирует семь маршрутов. В `openapi.yaml` есть четыре (`:28,71,147,204`). Не описаны `GET /api/v1/version` (`:661`), `GET /api/v1/metrics` (`:662`) и `GET /api/v1/audit` (`:663`), хотя структуры `VersionInfo`, `OpsMetrics` и `AuditPage` определены в том же файле `server.go:83-123`. Тест сверяет ответы только с описанными схемами и не проверяет полноту путей.

### 4. Клиент `human-decide` не умеет отправлять `obligationID`

Сервер принимает `obligationID` (`server.go:204`), `openapi.yaml:753-754` его документирует и приводит в примере (`:227`). Клиентская структура `decideWire` (`internal/infra/httpapi/client.go:561-567`) этого поля нет. `ClientDecision` (`:550-556`) тоже его не имеет. Значит `ProjectionClient.Decide` не может отправить обязательство, хотя контракт это позволяет. Обратная сторона: `ClientDecisionReceipt.Message` (`client.go:572-577`) отсутствует в `DecisionResult` сервера и в схеме openapi.

### 5. CLI `beacon human` принимает семь глаголов из девяти

`cmd/beacon/human.go:31-37` разрешает `approve`, `reject`, `rework`, `unblock`, `pause`, `resume`, `cancel`. Хендлер `server.go:927-931` и enum в `openapi.yaml:749` добавляют `context` и `force_retry_merge`. Строка usage в `cmd/beacon/main.go:49` повторяет семь. Команда, разрешённая протоколом, недоступна с CLI.

### 6. Каталог команд не совпадает со строкой usage

`cmd/beacon/catalog.go:20-36` содержит пятнадцать записей плюс `spawn` из `main.go:58-62`, итого шестнадцать. `usageText` в `main.go:23-50` перечисляет двадцать восемь строк, включая `what`, `doctor`, `build`, `test`, `gates`, `task`, `why`, `spawn`, `attach`, `registry stop`, `deliver`, `ci check`, `daemon`, `orchestrate`, `orchestrator bind`, `agents`, `watch`, `metrics`, `pr create`, `logs`, `inspect`, `human`, `reset`. Часть из них в каталоге есть, часть нет. Источник правды разбит на два файла, что прямо сказано в комментарии `main.go:55-57`.

### 7. Офлайн-гейт `tools/list` не подключён к `beacon gates`

Коммит `d3b1bfb` добавил `CheckToolContract` в `tools/beacon-gates/gate/tool_contract.go:73` с девятью тестами в `tool_contract_test.go`. Реестр `tools/beacon-gates/main.go:19-24` содержит только четыре гейта: `capabilities`, `layers-ast`, `layers-list`, `reachability`. Подкоманды `tool-contract` там нет. Функция существует и тестируется, но ни один прогон `beacon gates` её не вызывает. Защита, описанная в `docs/tasks/RS-MCP-02.md`, не работает как гейт.

### 8. Два HTTP-сервера с пересекающимися путями

Активный сервер в `internal/infra/httpapi` отдаёт `/api/v1/health`, `snapshot`, `events`, `human/decide`, `version`, `metrics`, `audit`. В `internal/infra/server` определены `/api/v1/watches`, `/api/v1/wake`, `/api/v1/questions/answer`, `/api/v1/approvals/respond`, `/api/v1/audit`, `/api/v1/events`, `/api/v1/ws` и `/mcp`. Пересечение по `audit` и `events`. `openapi.yaml` описывает поверхность первого сервера и не упоминает второй.

## Итоговая таблица

Риск дрейфа оценён по тому, что ловит существующая проверка. Приоритет P0 означает, что расхождение уже мешает агентам или оставляет публичный контракт без работающей защиты.

| Контракт | Источник правды | Защита | Риск дрейфа | Приоритет |
| --- | --- | --- | --- | --- |
| `tools/list`: имена, аннотации, размер 16095, SHA | `server.go:106`, `contract_test.go:24-50` | `TestToolsListContract`, `TestToolsListStabilityAcrossConnections`, ADR 0094 | Высокий: хеш ломается от любой правки описания, эталон обновляется руками | P0 |
| Порядок инструментов в `tools/list` | `contract_test.go:37-50` | Побайтовое сравнение двух соединений | Высокий: ADR 0094 и 0263 прямо отрицают порядок как контракт | P0 |
| `beacon.human-decide.v1` | `httpapi/server.go:60-63,201-220` | `TestContractDecideVersioned`, `client_test.go`, валидация по openapi | Средний: клиент не шлёт `obligationID`, квитанция имеет лишнее поле | P0 |
| `openapi.yaml` | `docs/api/openapi.yaml` | `loadOpenAPI`, `validateAllExamples`, `validateBody` | Высокий: 4 пути из 7, полнота путей не проверяется | P0 |
| ADR о числе инструментов | `docs/adrs/0094, 0263, 0186, 0155, 0156` | Только контрактный тест | Высокий: семь против восьми, 64 KiB против 16 KiB | P1 |
| Офлайн-гейт `tools/list` | `tools/beacon-gates/gate/tool_contract.go:73` | 9 юнит-тестов функции | Высокий: гейт не зарегистрирован в `beacon gates` | P1 |
| Второй HTTP `/api/v1` в `internal/infra/server` | `api_watches.go:22`, `api_wake.go:22`, `api_audit.go:23` | Юнит-тесты обработчиков | Высокий: не смонтирован, дублирует пути активного сервера | P1 |
| Каталог `beacon list --format json` | `catalog.go:20-36`, `main.go:58-62` | `TestT06ListJSONCatalog` только на валидный JSON | Средний: источник разбит на два файла, полнота не проверяется | P1 |
| JSON карточек `beacon task list --format json` | `docs/beacon-task-cli-spec.md:64-101`, `taskcli/service.go:50-60` | Тест на валидный JSON и число записей | Средний: имена полей и сортировка не зафиксированы | P1 |
| Схемы вывода MCP и `nextAction` | `mcp/schema.go:24-303` | `TestOutputSchemasValidateRealResponses` с probes на новые поля | Низкий: схемы выводятся из типов, тест допускает добавление полей | P2 |

## Что стоит сделать по порядку

1. Обновить `README.md`, `evals/main.go:43`, `docs/runbooks/verification.md:41` и ADR 0263, 0186, 0155, 0156 до восьми инструментов и 16 KiB. В ADR 0094 и 0263 согласовать утверждение о порядке имён с `contract_test.go:37-50`.
2. Добавить в `tools/beacon-gates/main.go:19-24` запись для `tool-contract` и данные ответа `tools/list`, иначе коммит `d3b1bfb` остаётся мёртвым кодом.
3. Дополнить `openapi.yaml` маршрутами `version`, `metrics`, `audit` и добавить тест, который сверяет множество зарегистрированных в `httpapi/server.go:660-666` путей с ключами `paths` в документе.
4. Добавить `obligationID` в `ClientDecision` и `decideWire` (`client.go:550-567`), убрать `Message` из `ClientDecisionReceipt` или добавить его в `DecisionResult` и openapi. Расхождение симметрично, выбор за владельцем контракта.
5. Решить судьбу `internal/infra/server`: удалить несмонтированные обработчики `/api/v1` или описать их во втором документе с явным указанием, какой сервер какой путь обслуживает.

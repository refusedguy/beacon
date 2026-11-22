# Исследование направления 4 после первого прохода PROTO

Отчёт проверяет состояние `origin/main` на коммите `b693e56d` в чистом worktree. Отчёт не пересказывает `docs/research/audit-04-protocols.md`, карточки PROTO-01..08, AUD-09/10/13 и BCX-41/45. Те документы и карточки уже в main и имеют своих владельцев файлов. Здесь зафиксировано только то, что влилось после аудита, и то, что аудит не описал.

Локальные `go test`, `go build`, `beacon gates` и `batch-apply` не запускались. Все числа прочитаны из исходников с номером строки. Экономия контекста не измерена, поэтому эффект ниже помечен как ожидаемый.

## 1. Проверка влитого

| Утверждение аудита 04 | Состояние на `b693e56d` | file:line и символ |
| --- | --- | --- |
| AGENTS.md называет 9 инструментов | Влито 10, `beacon_task_close` описан | `AGENTS.md:115`, фраза «Десять инструментов» |
| CycleInstructions не знает про task_claim и task_close | Влито 10, есть отдельный шаг закрытия | `internal/infra/mcp/server.go:93-105`, константа `CycleInstructions`, строка 105 «Ten tools» |
| Remote-карта знает 7 из 10 | Влито 10 имён и 10 фабрик схем | `internal/infra/mcp/client.go:290` `remoteToolDescriptions`, `:303` `remoteInputSchemaFactories`, `:350` цикл по `ToolNames` |
| Дрейф 10 против 11 | Одиннадцатое имя живёт в `AdvertisedToolNames` | `internal/infra/mcp/server.go:251` `ToolNames`, `:263` `AdvertisedToolNames`, `:266` `WakeToolName`; `tests/e2e/e2e_test.go:61` `expectedSet` |
| `/mcp` не смонтирован до PROTO-03 | Влито через таблицу дополнительных маршрутов | `internal/infra/server/mcp.go:21` `MCPExtraRoutes`, `:23` `MCPPath`; `internal/infra/httpapi/server.go:1004` цикл `ExtraRoutes`; `cmd/beacon/mcp.go:1107` передача `ExtraRoutes` |
| `graphReady` спрятан в строке | Типизированный сигнал появился, потребителя в production нет | `internal/infra/mcp/tool_stats.go:581` `GraphReadySignal`, `:602` `ReadGraphReady`, `:621` `StatsOutput.GraphReady`; вызовы только в тестах |
| `registryListSource` не привязан | Влито, источник строит reader с областью проекта | `cmd/beacon/registry_list.go:77` `registryListSource`, `:203` вызов |
| Четыре ACP-раннера | Стало пять, добавлен gemini | `internal/infra/wake/acp.go:425-431` `acpRuntimes`, строка 430 `"gemini"` |
| SSE несёт только `Type string` | Типизированная обёртка появилась, производители её не используют | `internal/infra/httpapi/server.go:327-333` `Event`, `:78` `EventContract`, `:340` `EventName`, `:544` `PublishTyped`; `cmd/beacon/mcp.go:1013`, `:1969`, `cmd/beacon/spawn.go:480` |
| A2A, UTCP, Code Mode отвергнуты | Решение зафиксировано в ADR | `docs/adrs/0310-a2a-utcp-code-mode-go.md:1`, `:11` |

Вывод по таблице простой. Списковый дрейф 7/9/10/11 устранён. Две проблемы остались в виде частично влитого кода. Типизированный SSE-контракт не имеет production-производителя. Типизированный `graphReady` не имеет production-потребителя.

## 2. Каналы для десяти инструментов

Решение остаётся за ADR 0308 (`docs/adrs/0308-beacon-mcp-cli-push.md:1`). Таблица ниже не меняет решение, она сверяет его с текущими строками. Внутри хода агента остаются инструменты с атомарной записью и типизированной ошибкой. Диагностика доступна в CLI. Ожидание заменяется wake, после которого агент читает вердикт малым вызовом.

| Инструмент | Канал | file:line и символ | Почему |
| --- | --- | --- | --- |
| `beacon_watch_pr` | MCP | `internal/infra/mcp/server.go:148` `Name: "beacon_watch_pr"` | Регистрация Watch и подписка идут одним вызовом |
| `beacon_wait_result` | MCP как режим совместимости, основной канал wake | `internal/infra/mcp/server.go:159`, `:160` описание лизинга на 30 минут; `GLOSSARY.md:47` `Ladder`, `:105` `Wake` | Долгий опрос держит лизинг, цикл велит спать в `server.go:96-99` |
| `beacon_get_result` | MCP | `internal/infra/mcp/server.go:168` | Малое чтение после пробуждения |
| `beacon_list_running` | CLI и SKILL.md | `internal/infra/mcp/server.go:179`; `cmd/beacon/catalog.go:35,59` `board`, `watch` | Диагностика по требованию |
| `beacon_ack` | MCP | `internal/infra/mcp/server.go:188` | Идемпотентная запись `claimedAt` и решения `blocked:not_mine` |
| `beacon_cancel_watch` | MCP | `internal/infra/mcp/server.go:202` | Единственный деструктивный инструмент, нужен явный вызов |
| `beacon_stats` | CLI и SKILL.md | `internal/infra/mcp/server.go:211`; `cmd/beacon/catalog.go:47` `inspect`, `:49` `logs` | Счётчики читает диагностика |
| `beacon_defer_wiring` | MCP | `internal/infra/mcp/server.go:220` | Возвращает готовую карточку в точке хода |
| `beacon_task_claim` | MCP | `internal/infra/mcp/server.go:229` `TaskClaimToolName` | Атомарная выдача карточки, гонка двух агентов недопустима |
| `beacon_task_close` | MCP | `internal/infra/mcp/server.go:238` `TaskCloseToolName` | Закрытие с проверкой владельца и идемпотентностью |
| `beacon_wake` | Канал адаптера, не каталог агента | `cmd/beacon/mcp.go:520` `AttachWake`; `internal/infra/mcp/server.go:266` `WakeToolName` | Входит в `AdvertisedToolNames`, но не в `toolDescriptors` |

Экономия контекста от переноса двух read-инструментов в CLI остаётся ожидаемой. Исторические замеры tools/list лежат в `docs/adrs/0268-task-claim-tool.md:25` и `docs/adrs/0270-task-close-tool.md:9`. Новый замер без CI не снимался.

## 3. Zed ACP и AG-UI на текущем коде

### ACP

ACP остаётся каналом хост и агент. Клиентская сторона завершена. `internal/infra/wake/acp.go:425-431` `acpRuntimes` содержит пять команд, `:455-457` `ResolveACPRuntimeCommand` разрешает заменить бинарник и аргументы, `:714` `Initialize` согласует capabilities, `:757` `DeliverWake` выбирает resume или новый сеанс, `:808` `Prompt` шлёт текст. Альтернативные бинарники вроде `claude-agent-acp` и `codex-acp` подключаются полем `Command` без нового протокола.

Новая находка: объявленная capability `mcp` не имеет моста. `internal/infra/wake/acp.go:239` объявляет `ACPCapMCPTransport`, `:316` и `:329` разбирают и включают её, но `:782` `NewSession` и `:802` `LoadSession` передают `"mcpServers": []any{}`. Агент получает wake текстом, а каталог инструментов через ACP не приходит. Это не дублирует PROTO-06, который запрещает новые протоколы в ядре. Здесь речь о пустом аргументе уже существующего протокола.

### AG-UI

Типизированный слой появился, но работает только в тестах. `internal/infra/httpapi/server.go:78` задаёт `EventContract = "beacon.events.v1"`, `:340-354` задаёт закрытый набор `EventName`, `:383-390` определяет `TypedEvent`, `:544` `PublishTyped` проверяет имя и полезную нагрузку, `:517` `publishLocked` пишет кадр в шину. Production-вызовов `PublishTyped` нет. Три производителя шлют имена вне закрытого набора: `cmd/beacon/mcp.go:1013` `human.decide`, `cmd/beacon/mcp.go:1969` `cycle`, `cmd/beacon/spawn.go:480` `session.update`. На одной шине живут две таксономии событий. Подписчик не может доказать, что кадр относится к типизированному контракту, и продолжает разбирать `json.RawMessage` вручную.

## 4. Что не тащить в Go-ядро

Отказ уже зафиксирован в `docs/adrs/0310-a2a-utcp-code-mode-go.md:1,11`. Проверка по текущему коду подтверждает его.

A2A для локального запуска не нужен. Все акторы живут на одной машине. Один loopback-сервер хранит токен в `connection.json` с режимом `0600` (`internal/infra/httpapi/server.go:38-39` `connectionFileName`, `connectionPerm`), а bearer сравнивается в константное время в `:1010-1013` `bearer`. A2A добавит второй язык описания задач и вторую модель авторизации.

Тяжёлый Code Mode внутри Go не нужен. Среда агента уже вызывает инструменты как `tools.beacon.<name>` после `search`, `AGENTS.md:115`. Сервер отдаёт фиксированные схемы через `internal/infra/mcp/server.go:146` `toolDescriptors`. Песочница JavaScript или динамический реестр внутри Go повторит работу среды агента и добавит зависимость.

UTCP вместо аутентифицированного шлюза не нужен. У Beacon уже есть один JSON-RPC формат вызовов (`internal/infra/mcp/client.go:5`, `:146` `JSONRPC`, `:168` `CallTool`) и один bearer-шлюз. Добавление `/utcp` создаст второй каталог инструментов и вторую историю авторизации. Решение остаётся в силе, новой карточки не требуется.

## 5. Нарезка бэклога

P0. Новых карточек нет. Списковый дрейф, `/mcp`, remote-карта и реестр уже влиты, строки указаны в разделе 1. Локальные проверки не запускались, поэтому новый P0 по красному CI не объявляется.

| Приоритет | Карточка | Находка, baseline | OwnerFiles, существуют в main |
| --- | --- | --- | --- |
| P1 | PR4-01 | Две таксономии SSE. 0 вызовов `PublishTyped` вне тестов против 3 legacy-издателей в `cmd/beacon/mcp.go:1013,1969` и `cmd/beacon/spawn.go:480` | `cmd/beacon/mcp.go`, `cmd/beacon/spawn.go` |
| P1 | PR4-02 | Пустой мост ACP. 1 capability `mcp` в `internal/infra/wake/acp.go:239` против 2 вызовов с `"mcpServers": []any{}` в `:782,802` | `internal/infra/wake/acp.go`, `internal/infra/wake/acp_smoke_test.go` |
| P2 | PR4-03 | Типизированный `graphReady` без потребителя. 1 сигнал в `internal/infra/mcp/tool_stats.go:581,602,621`, 0 вызовов вне тестов | `cmd/beacon/inspect.go`, `cmd/beacon/inspect_test.go` |
| P2 | Без карточки | A2A, UTCP и тяжёлый Code Mode уже отклонены ADR 0310 | Владельцев файлов не меняем |

OwnerFiles трёх карточек не пересекаются. В P1 файлы `cmd/beacon/mcp.go` и `cmd/beacon/spawn.go` не совпадают с файлами ACP-карточки. В P2 файлы `cmd/beacon/inspect.go` и `cmd/beacon/inspect_test.go` не совпадают с файлами P1. Карточки не трогают владельцев PROTO-01..08, AUD-09/10/13 и BCX-41/45.

## 6. Проверка артефактов

Проверка JSON и содержимого коммита выполняется командами `python3 -m json.tool docs/research/research-dir4-protocols-batch.json > /dev/null` и `git cat-file -p HEAD:<path>`. Наличие OwnerFiles проверяется явными чтениями файлов. Go-команды не запускались.

## Топ-5

1. `graphReady` имеет typed signal, но не имеет вызывающего кода вне тестов: `internal/infra/mcp/tool_stats.go:581,602,621`.
2. ACP объявляет capability `mcp`, но шлёт пустой `mcpServers`: `internal/infra/wake/acp.go:239,782,802`.
3. Типизированный SSE-контракт живёт отдельно от трёх legacy-издателей: `internal/infra/httpapi/server.go:78,340,544` и `cmd/beacon/mcp.go:1013,1969`, `cmd/beacon/spawn.go:480`.
4. `/mcp` влит и смонтирован через `ExtraRoutes`: `internal/infra/server/mcp.go:21-24`, `internal/infra/httpapi/server.go:1004`, `cmd/beacon/mcp.go:1107`.
5. ACP-раннеров стало пять, альтернативные бинарники подключаются без нового протокола: `internal/infra/wake/acp.go:425-431,455-457`.

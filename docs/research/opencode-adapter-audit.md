# Аудит адаптера OpenCode: текущая архитектура и замена на встроенный API

Дата: 2026-11-21. Режим только чтение. Код, конфиги и тесты не менялись. `go test`, `go build`, `beacon gates` и `tools.beacon.*` не запускались. Поиск шёл через `rg` с явными каталогами `cmd/beacon/`, `internal/infra/opencode/`, `internal/infra/wake/`, `docs/` и `tests/e2e/`.

## Короткий ответ

Beacon держит два независимых пути к OpenCode. Первый это тонкий HTTP-клиент в `internal/infra/opencode/client.go`, который сам читает `service.json` и сам ходит по HTTP. Второй это `opencode api` CLI, уже используемый в wake как фолбэк и в командах `watch` и `agents` как основной путь. Оба пути делают одно и то же: читают список сессий и пишут промпт.

Третий путь это управление сервером в `internal/infra/opencode/server.go`. Функция `EnsureServer` спавнит `opencode serve` на свободном порту, пишет свой `opencode-serve.json` и опрашивает health. OpenCode уже делает всё это сам через discovery `service.json` и команду `opencode api`. Управление процессом со стороны Beacon дублирует встроенный механизм и приводит к смене процессов, описанной в `docs/research/mcp-q1-startup.md:51-59` и `docs/research/process-leak-mechanism.md:46-56`.

Замена возможна почти везде. Исключение это SSE. E2E-тест фиксирует, что `opencode api` CLI в v2.3.0 не отдаёт тело `/api/event`, а возвращает ноль байт (`tests/e2e/e2e_test.go:482-485`). Для потока событий нужен прямой HTTP-клиент, но только для чтения и без спавна.

## 1. Инвентарь портов и адаптеров

### Инфраструктура

| Файл:строки | Что делает |
| --- | --- |
| `internal/infra/opencode/client.go:1-8` | Пакет объявлен как тонкий читатель HTTP API по адресу из `service.json`. Не знает имён домена Beacon. Живёт в `infra`, потому что `net/http` запрещён в `cmd/beacon`. |
| `internal/infra/opencode/client.go:27-42` | Константы: Basic-пользователь `opencode`, TTL кэша сессий 2 с, пауза переподключения 500 мс, лимиты SSE-кадра и HTTP-тела по 1 МиБ. |
| `internal/infra/opencode/client.go:47-81` | `Config{URL, Password}` и `ReadConfig()`. Читает `$XDG_STATE_HOME/opencode/service.json` с фолбэком на `~/.local/state`. |
| `internal/infra/opencode/client.go:83-133` | `Doer` как единственный сетевой шов и `New(cfg, doer)`. Пустой `doer` превращается в `&http.Client{}` без таймаута, потому что SSE держит соединение долго. |
| `internal/infra/opencode/client.go:135-204` | Типы ответов: `Agent`, `modelID` с обратной совместимостью к строке и объекту v2, `Session`, обёртка `list[T]` с полями `data` и `location`. |
| `internal/infra/opencode/client.go:206-298` | `get`, `post`, `decodeList`, `decodeSingle`. Basic-авторизация, проверка статуса, ограничение размера тела. |
| `internal/infra/opencode/client.go:300-399` | Методы HTTP API: `ListAgents`, `Sessions`, `CreateSession`, `Prompt`, `Config`. |
| `internal/infra/opencode/client.go:401-546` | `Event`, `EventResult`, `EventStream`, `openEvent`, `waitReconnect`, `consumeEvent`. SSE с разбором строк `data:` и переподключением без replay. |
| `internal/infra/opencode/server.go:26-44` | Константы спавна: loopback `127.0.0.1`, health-путь `/api/config`, таймаут 30 с, интервал опроса 100 мс, файл `opencode-serve.json`. |
| `internal/infra/opencode/server.go:46-128` | `spawnRequest`, `spawnedProcess`, `serveRunner`, `startServer`, `execProcess`, `startServeProcess`. Команда `opencode serve --hostname ... --port ...`, пароль через `OPENCODE_SERVER_PASSWORD`, `Pdeathsig: SIGTERM`. |
| `internal/infra/opencode/server.go:130-191` | `freePort()` через `net.Listen(:0)`, `randomPassword()`, `healthReady()`, `waitHealthy()`. |
| `internal/infra/opencode/server.go:193-281` | `ServeState{URL, Password, PID}` и функции чтения, записи и очистки файла состояния. |
| `internal/infra/opencode/server.go:283-323` | `ReadServicePID`, `ProcessAlive` через сигнал 0, `TerminateServe` через SIGTERM. |
| `internal/infra/opencode/server.go:325-385` | `ServeProcess`, `ListServeProcesses()` со сканированием `/proc`, `parseServeCmdline()`. |
| `internal/infra/opencode/server.go:387-429` | `EnsureServer()`. Сначала свой живой сервер, потом `service.json`, потом спавн. |
| `internal/infra/opencode/server.go:431-679` | `ScopeTokenStore` и токены областей wake, read, stop. Отдельная задача, к discovery отношения не имеет. |

### Порты в `cmd/beacon`

| Файл:строки | Порт и установщик |
| --- | --- |
| `cmd/beacon/opencode.go:14-21` | `opencodePort`: `ListAgents`, `Sessions`, `Config`, `EventStream`. `var _ opencodePort = (*opencode.Client)(nil)`. |
| `cmd/beacon/opencode.go:23-46` | `clientSessionCreator`. Адаптер сбрасывает `context` и возвращает только `session.ID`. |
| `cmd/beacon/opencode.go:48-56` | `sessionCreator`: `CreateSession` и `Prompt`. |
| `cmd/beacon/opencode.go:58-75` | `serverEnsurer` и `serverEnsurerFunc`. Обёртка над пакетной функцией `EnsureServer`. |
| `cmd/beacon/opencode.go:77-107` | `installedServerEnsurer`, `SetSessionCreator`, `SetServerEnsurer`, `resolveOpencodeConfig`. Выбор между ensurer и `ReadConfig`. |
| `cmd/beacon/opencode.go:109-129` | `opencodeHTTPDoer` и `newOpencodePort()`. Строит клиент и ставит `SetSessionCreator` и `SetServerEnsurer(opencode.EnsureServer)`. |
| `cmd/beacon/main.go:116-137` | `main()` вызывает `wireOpenCodePorts()` до разбора команд. Цепочка: `newOpencodePort()` и `installOpenCodePorts()`. |
| `cmd/beacon/main.go:139-159` | `installOpenCodePorts()`. Ставит `liveSource`, `sessionSpawner`, `deliveryTarget`, `sessionCreator`, `serverEnsurer` через type assertion. |
| `cmd/beacon/main.go:161-220` | `liveOpencodeSource`. Переводит `Sessions` в строки таблицы и `EventStream` в `liveEvent`. |
| `cmd/beacon/watch.go:289-316` | `liveEvent`, `liveSource`, `watchLiveSource`, `SetLiveSource`. |
| `cmd/beacon/watch.go:386-459` | `watchRender()` и `runWatchLive()`. Каждое событие перечитывает снимок и список сессий. |
| `cmd/beacon/spawn.go:21-45` | `sessionSpawner`, `spawnSessionSpawner`, `SetSessionSpawner`. Те же два метода, что и у `sessionCreator`. |
| `cmd/beacon/deliver.go:37-58` | `deliveryTarget`, `deliveryTargetFunc`, `deliverTarget`, `SetDeliveryTarget`. Один метод `Prompt`. |
| `cmd/beacon/observe.go:282-290` | `observingDeliveryTarget`. Обёртка над доставкой для метрик. |

### Wake и CLI

| Файл:строки | Что делает |
| --- | --- |
| `internal/infra/wake/deliver.go:26-40` | `PromptFunc` и `SessionActiveFunc` как порты доставки. |
| `internal/infra/wake/deliver.go:50-68` | `ErrNoPromptPort`, `ErrSessionDead`, таймаут CLI 10 с. |
| `internal/infra/wake/deliver.go:270-314` | `Deliver()`. Сначала `PromptFunc`, при `ErrNoPromptPort` уходит в CLI. |
| `internal/infra/wake/deliver.go:316-343` | `deliverCLI()`. Команда `opencode api POST /api/session/<id>/prompt --data ...`. |
| `internal/infra/wake/deliver.go:839-906` | `T202Runner` и `T202API`. Prompt, Park, Interrupt, Promote через `opencode api`. |
| `cmd/beacon/mcp.go:1513-1542` | `opencodeWakePorts()`. Строит `PromptFunc` и `SessionActiveFunc` из `newOpencodePort()`. |
| `cmd/beacon/mcp.go:1544-1556` | `reconcileWaker()`. Регистрирует deliverer. Вызовы на строках 906, 1586, 1664, 1732. |
| `cmd/beacon/watch.go:98-115` | `parseSessions()`. Разбор вывода `opencode api session.list`. |
| `cmd/beacon/watch.go:238-244` | Не-live ветка вызывает `c.run(..., "opencode", []string{"api", "session.list"}, nil)`. |
| `cmd/beacon/agents.go:208-226` | Сначала `opencode api app.agents`, при ошибке `opencode api session.list`. |
| `cmd/beacon/agents.go:303` | `parseAgentRowsFromSessions()`. Второй разбор того же ответа. |
| `cmd/beacon/registry_sync.go:292-296` | `registrySyncOpenSource()` вызывает `newOpencodePort()`, то есть снова `resolveOpencodeConfig`. |

## 2. Какие методы дёргают HTTP и с какой конфигурацией

Прямые HTTP-вызовы живут только в `internal/infra/opencode`. `cmd/beacon` не открывает сокеты. Адрес и пароль приходят из одного места: `ReadConfig()` в `client.go:57-81`.

| Метод | Маршрут | Где вызывается в продакшене |
| --- | --- | --- |
| `ListAgents` | `GET /api/agent` | Только в тестах. В `cmd/beacon` не вызывается. `agents.go` ходит через CLI. |
| `Sessions` | `GET /api/session` | `liveOpencodeSource.SessionList()` в `main.go:179`, `opencodeWakePorts.active` в `mcp.go:1529`, `registry sync` через порт. Кэш 2 с в `client.go:315-340`. |
| `CreateSession` | `POST /api/session` | `clientSessionCreator.CreateSession()` в `opencode.go:35-41`, оттуда `beacon spawn`. |
| `Prompt` | `POST /api/session/:id/prompt` | `clientSessionCreator.Prompt()` в `opencode.go:44-46`, `beacon deliver`, `opencodeWakePorts.prompt` в `mcp.go:1525-1527`. |
| `Config` | `GET /api/config` | Только в тестах. В продакшене используется как health-путь спавна в `server.go:33`. |
| `EventStream` | `GET /api/event?types=...` | `liveOpencodeSource.Events()` в `main.go:197-220`. Типы в `main.go:173`: `session.status` и `session.execution.started`. |

Конфигурация берётся из `$XDG_STATE_HOME/opencode/service.json` в четырёх местах с разным набором полей:

1. `internal/infra/opencode/client.go:57-81` читает `url` и `password`, проверяет scheme и host.
2. `internal/infra/opencode/server.go:226-232` и `:283-301` читают путь и `pid`.
3. `cmd/beacon/agents.go:131-164` читает `url`, `pid`, `version` и только проверяет наличие файла.
4. `cmd/beacon/attach.go:44-74` читает только `url`.
5. `cmd/beacon/doctor.go:297-306` считает базовый каталог состояния.

Все пять вариантов строят один и тот же путь `filepath.Join(base, "opencode", "service.json")`. Все пять повторяют фолбэк на `~/.local/state`.

`EnsureServer` добавляет шестую конфигурацию: свой файл `$XDG_STATE_HOME/beacon/opencode-serve.json` с полями `url`, `password`, `pid` (`server.go:193-201`, `:218-224`). Он живёт рядом с пользовательским `service.json` и не является частью OpenCode.

## 3. Где Beacon изобретает свой механизм

### Спавн сервера

`EnsureServer` в `server.go:387-429` повторяет то, что в OpenCode делает `Service.ensure()` и команда `opencode api`. Beacon сам выбирает порт через `net.Listen(:0)`, сам генерирует пароль, сам запускает процесс, сам ждёт готовности, сам пишет состояние и сам завершает процесс по SIGTERM. Документ `docs/research/opencode-experimental-wake-api.md:48-71` показывает, что встроенный discovery читает тот же файл, делает health-probe и не спавнит процесс. Спавн делает `Service.ensure()`, и `opencode api` вызывает его сам, если здорового сервиса нет.

Практическое следствие зафиксировано в `docs/research/mcp-q1-startup.md:51-59`. Старт `beacon mcp` строит порт до первого вызова инструмента. Ensurer становится не-nil. Если позже `service.json` исчезает, следующий вызов с `cmd/beacon/mcp.go:1517` уходит в `EnsureServer` и поднимает новый `opencode serve`. Это объясняет постоянную смену процессов из `docs/research/process-leak-mechanism.md`.

`ListServeProcesses()` в `server.go:325-359` сканирует `/proc` и разбирает `cmdline`. Чтобы узнать, какой сервер работает, у OpenCode есть `opencode service status`. GC в `cmd/beacon/gc.go:30-37` подключён к шести функциям из `server.go`, включая `TerminateServe`. Это ещё один собственный механизм управления чужим процессом.

### Дублирование портов

Три интерфейса описывают одну и ту же пару методов.

- `sessionSpawner` (`spawn.go:31-34`): `CreateSession`, `Prompt`.
- `sessionCreator` (`opencode.go:53-56`): `CreateSession`, `Prompt`.
- `deliveryTarget` (`deliver.go:41-43`): `Prompt`.

`clientSessionCreator` в `opencode.go:23-46` существует только для того, чтобы убрать `context` из сигнатуры и вернуть строку вместо `Session`. `SetSessionCreator` в `opencode.go:85-93` распределяет один объект сразу в два порта. `installOpenCodePorts` в `main.go:145-159` делает это ещё раз через type assertion. chmod setup: четыре установщика для одного клиента.

Методы `ListAgents` и `Config` объявлены в `opencodePort` (`opencode.go:14-19`), но в продакшене не вызываются. Остались только тесты `opencode_test.go:78`, `:243` и `client_test.go:131`, `:263`.

### Дублирование чтения сессий

Один и тот же ответ `session.list` разбирается трижды: `client.go:315-340` через `decodeList`, `watch.go:98-115` через `parseSessions`, `agents.go:303` через `parseAgentRowsFromSessions`. Live-ветка `watch` использует HTTP-клиент, не-live ветка на строке 238 вызывает CLI. При выключенном сервисе это два разных пути к одним данным.

### Две доставки wake

`OpenCodeDeliverer` имеет HTTP-порт `PromptFunc` и CLI-фолбэк `deliverCLI`. Оба пишут в сессию через `POST /api/session/:id/prompt`. Разница только в транспорте. `T202API` в `wake/deliver.go:842-906` делает то же через `opencode api` и добавляет interrupt и promote. По существу в wake уже лежит готовый адаптер на встроенном API, а HTTP-порт поверх него это лишний слой.

## 4. Что можно заменить на встроенный API OpenCode

Здесь «встроенный API» означает три вещи, описанные в `docs/research/opencode-experimental-wake-api.md:21-76`: experimental HTTP API OpenCode, команду `opencode api` и discovery `Service.discover()` по `service.json`.

| Сейчас | Цель | Доказательство |
| --- | --- | --- |
| `EnsureServer`, `startServeProcess`, `freePort`, `randomPassword`, `waitHealthy`, `ServeState`, `ListServeProcesses`, `TerminateServe` | Полностью убрать. Команда `opencode api` сама находит сервис и при необходимости запускает его по политике OpenCode. | `docs/research/opencode-experimental-wake-api.md:38-71`, `docs/design/opencode-cli-manager.md:11-21` |
| `ReadConfig` в пяти копиях | Одна функция `Discover()` в `internal/infra/opencode`, читающая `service.json`. Для CLI-команд достаточно конфига без пароля. | `docs/adrs/0092-background-server-source.md:9-14` |
| `Sessions`, `ListAgents` через HTTP | `opencode api session.list`. Уже работает в `watch`, `agents` и `registry sync`. | `docs/adrs/0092-background-server-source.md:7-13`, `cmd/beacon/watch.go:238` |
| `CreateSession`, `Prompt` через HTTP | `opencode api POST /api/session` и `POST /api/session/:id/prompt`. Уже реализовано в `T202API`. | `docs/adrs/0194-cli-opencode-api.md:11`, `internal/infra/wake/deliver.go:854-874` |
| `Interrupt`, `Promote`, ожидание сессии | `opencode api` с operation ID. Маршруты `interrupt`, `experimental.session.wait` и inbox доступны тому же CLI. | `docs/adrs/0194-cli-opencode-api.md:11-13` |
| `EventStream` через HTTP | Оставить. `opencode api` не отдаёт тело SSE в v2.3.0. Но убрать отсюда ensurer и оставить только чтение адреса. | `tests/e2e/e2e_test.go:482-485` |
| `sessionSpawner`, `sessionCreator`, `deliveryTarget`, `clientSessionCreator` | Один порт `opencodePort` с методами `Sessions`, `CreateSession`, `Prompt`, `Events`. Подписи с `context` оставить как есть. | Раздел 3 выше |
| `serverEnsurer`, `serverEnsurerFunc`, `installedServerEnsurer`, `resolveOpencodeConfig`, `SetServerEnsurer` | Удалить. Решение о запуске сервера принадлежит OpenCode. | `docs/research/mcp-lazy-port-design.md:51-57` |

Точечный первый шаг уже спроектирован в `docs/research/mcp-lazy-port-design.md:90-112`. Это `newReadOnlyOpencodePort()` для `cmd/beacon/mcp.go:1517`. Он убирает спавн только из wake-пути и не трогает остальные команды. Для полной замены этого недостаточно, но как отдельный проверяемый шаг он подходит.

Что остаётся за рамками замены. `ScopeTokenStore` в `server.go:431-679` хранит токены областей и не знает про discovery. Его можно оставить на месте или вынести в отдельный файл, но это не часть адаптера OpenCode. Experimental-префикс `/api/experimental/*` относится к файловым и сессионным маршрутам OpenCode, а не к особому режиму wake (`docs/research/opencode-experimental-wake-api.md:25-36`).

## 5. Целевая схема адаптера

Один пакет `internal/infra/opencode` и один порт в `cmd/beacon`. CLI становится тонкой обёрткой над `opencode api`. Прямой HTTP остаётся только для SSE.

```
                     cmd/beacon (composition root)
                     main.go: wireOpenCodePorts()
                                  |
                                  v
                  opencodePort (один интерфейс)
              Sessions / CreateSession / Prompt / Events
                                  |
              +-------------------+-------------------+
              |                                       |
              v                                       v
   opencode api runner                     SSE reader (read-only)
   internal/infra/opencode                 internal/infra/opencode
   Runner func(ctx, args...) []byte        Discover() -> url, password
              |                                       |
              v                                       v
   opencode api <operation>                 GET /api/event?types=...
   session.list, POST /api/session,          Basic auth, без спавна
   prompt, interrupt, promote
              |
              v
   discovery и авторизация внутри OpenCode
   $XDG_STATE_HOME/opencode/service.json
```

Распределение ответственности после перестройки.

1. `internal/infra/opencode/api.go`, новый файл. `Runner` как шов для тестов, методы `Sessions`, `CreateSession`, `Prompt`, `Interrupt`, `Promote`. Все через `opencode api`. Возвращают нейтральные структуры без доменных имён Beacon.
2. `internal/infra/opencode/discovery.go`. Одна функция `Discover()`, читающая `service.json`. Никакой записи файлов и никаких `exec`.
3. `internal/infra/opencode/client.go`. Остаются только `Config`, `New`, `get` и `EventStream`. Удаляются `ListAgents`, `Sessions`, `CreateSession`, `Prompt`, `Config` как methods прямого HTTP и TTL-кэш, если `session.list` переходит в CLI.
4. `internal/infra/opencode/server.go`. Удаляется весь спавн-блок на строках 26-429. Остаётся `ScopeTokenStore` с 431 строки. GC переходит на `opencode service status` и `opencode service stop` или теряет serve-ветку совсем.
5. `cmd/beacon/opencode.go`. Один `opencodePort`, один конструктор, один `SetOpenCodePort`. Удаляются `serverEnsurer`, `clientSessionCreator` и `resolveOpencodeConfig`.
6. `cmd/beacon/main.go`. `installOpenCodePorts()` ставит один объект. `liveOpencodeSource` остаётся как адаптер данных для `watch`. События он получает от SSE-reader.
7. `cmd/beacon/spawn.go`, `deliver.go`, `watch.go`.
8. `cmd/beacon/mcp.go`. `opencodeWakePorts()` строит замыкания поверх нового порта. `PromptFunc` и `SessionActiveFunc` можно удалить, если deliverer примет один порт. CLI-фолбэк `deliverCLI` становится основным и единственным путём, `ErrNoPromptPort` теряет смысл.
9. `cmd/beacon/agents.go`, `attach.go`, `gc.go`, `doctor.go`. Все чтения `service.json` идут через `Discover()`. Локальные `opencodeServiceFile`, `serviceConfigPath`, `stateBaseDir` удаляются.

Порядок проверяемых шагов.

1. Добавить `newReadOnlyOpencodePort()` и перевести `mcp.go:1517`. Проверить по тексту, что `EnsureServer` в wake-цепочке недостижим.
2. Перевести `opencode api session.list` в общий runner и убрать `parseSessions` и `parseAgentRowsFromSessions`, оставив один разбор.
3. Свести три порта записи к одному и удалить `clientSessionCreator`.
4. Убрать спавн из `server.go` и переписать GC.
5. Оставить SSE-клиент без `EnsureServer` и проверить, что он читает только адрес из `Discover()`.

## 6. Список файлов и строк

Ключевые точки для правки.

- `internal/infra/opencode/client.go:47-133`: конфиг, клиент, Basic auth.
- `internal/infra/opencode/client.go:300-399`: HTTP-методы, которые переходят в `opencode api`.
- `internal/infra/opencode/client.go:401-546`: SSE, который остаётся.
- `internal/infra/opencode/server.go:26-429`: весь собственный спавн и управление процессом.
- `internal/infra/opencode/server.go:431-679`: токены областей, не трогать.
- `internal/infra/wake/deliver.go:31-40`, `:270-343`, `:839-906`: порты доставки и готовый CLI-адаптер.
- `cmd/beacon/opencode.go:14-129`: все порты, адаптеры и `newOpencodePort`.
- `cmd/beacon/main.go:116-220`: composition root и `liveOpencodeSource`.
- `cmd/beacon/mcp.go:1513-1556`, `:906`, `:1586`, `:1664`, `:1732`: wake-порты и четыре точки сборки registry.
- `cmd/beacon/watch.go:98-115`, `:238-244`, `:289-316`, `:386-459`: разбор `session.list` и live-цикл.
- `cmd/beacon/spawn.go:21-45`: `sessionSpawner`.
- `cmd/beacon/deliver.go:37-58`: `deliveryTarget`.
- `cmd/beacon/observe.go:282-290`: обёртка метрик над доставкой.
- `cmd/beacon/agents.go:124-164`, `:208-226`, `:303`: чтение registration и вызовы CLI.
- `cmd/beacon/attach.go:44-74`: второе чтение registration.
- `cmd/beacon/gc.go:28-37`: шесть швов в спавн-механизм.
- `cmd/beacon/registry_sync.go:292-296`: третий вызов `newOpencodePort`.
- `cmd/beacon/doctor.go:297-306`: расчёт каталога состояния.
- `tests/e2e/e2e_test.go:482-485`: ограничение SSE через `opencode api`.
- `docs/research/mcp-lazy-port-design.md:90-112`: готовый точечный вариант A.
- `docs/research/opencode-experimental-wake-api.md`: первичные источники по discovery и `opencode api`.

## Источники

Локальные файлы перечислены в таблицах и в разделе 6. Первичные источники OpenCode по discovery, `opencode api` и registration-файлу приведены в `docs/research/opencode-experimental-wake-api.md:141-174`. Ограничение SSE подтверждено локальным e2e-тестом, а не утверждением в документации.

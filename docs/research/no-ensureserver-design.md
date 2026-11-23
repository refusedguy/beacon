# Дизайн: убрать EnsureServer и спавн opencode serve из beacon

Дата: 2026-11-21. Режим только чтение. Код, конфиги и тесты не менялись. `go test`, `go build`, `beacon gates` и `tools.beacon.*` не запускались. Поиск шёл через `rg` с явными каталогами `cmd/beacon/`, `internal/infra/opencode/`, `internal/infra/wake/`, `docs/`, `tests/e2e/`. Бинарник `opencode` опрашивался только командами чтения: `api --help`, `--help`, `service --help`, `serve --help`, `api session.list` и изолированный запуск с пустым `XDG_STATE_HOME`. Все временные каталоги и процессы удалены после замера.

Этот документ отвечает на пять вопросов задачи. Он опирается на `docs/research/opencode-adapter-audit.md` и `docs/research/opencode-experimental-wake-api.md`, но добавляет локальный замер поведения `opencode api` и точный patch-план.

## 1. Что изучено

### Discovery и клиент

`internal/infra/opencode/client.go` содержит `Config{URL, Password}` (строки 47-53) и `ReadConfig()` (57-81). Функция читает `$XDG_STATE_HOME/opencode/service.json` с фолбэком на `~/.local/state/opencode/service.json`, проверяет JSON и непустой `url`. Пароль берётся из того же файла. `New(cfg, doer)` (104-133) строит клиент с Basic-авторизацией `opencode:<password>`. Прямые HTTP-методы лежат в 300-399, SSE-читатель в 401-546. Ни один из этих путей не запускает процесс.

Локальный файл на момент замера: `{"id":"6147ce3a-...","version":"2.3.2","url":"http://127.0.0.1:49374","pid":3047150,"password":"..."}`. Beacon использует из него только `url` и `password`. PID из того же файла читает `ReadServicePID()` в `server.go:283-301`, а `gc.go:32` использует его для защиты живого сервера.

### Спавн в `internal/infra/opencode/server.go`

Блок 26-191 отвечает за спавн. Константы задают loopback `127.0.0.1`, health-путь `/api/config`, таймаут 30 с и интервал 100 мс. `startServeProcess()` (103-128) собирает `exec.Command("opencode", "serve", "--hostname", ..., "--port", ...)`, передаёт пароль через `OPENCODE_SERVER_PASSWORD`, направляет вывод в `io.Discard` и ставит `Pdeathsig: syscall.SIGTERM`. `freePort()` (132-143) выделяет порт через `net.Listen(:0)`. `healthReady()` и `waitHealthy()` (154-191) опрашивают `/api/config` до готовности.

`ServeState{URL, Password, PID}` (193-201) и функции `ReadServeState`, `WriteServeState`, `ClearServeState` (234-281) хранят запись сервера, который Beacon спавнил сам, в `$XDG_STATE_HOME/beacon/opencode-serve.json` с режимом 0600. `ListServeProcesses()` (325-385) сканирует `/proc` ради GC. `TerminateServe()` (313-323) шлёт SIGTERM.

`EnsureServer()` (387-429) объединяет всё в одну цепочку. Сначала он берёт живой сервер из `opencode-serve.json` (394-396), затем `service.json` (397-399), и только при отсутствии обоих источников выделяет порт, генерирует пароль, спавнит процесс, ждёт готовность и пишет состояние (401-428). `ScopeTokenStore` в 431-679 не связан с discovery и должен остаться.

### Как Beacon доходит до EnsureServer

В `cmd/beacon/opencode.go` порт `serverEnsurer` (58-75) оборачивает пакетную функцию. Глобал `installedServerEnsurer` и `SetServerEnsurer` живут в 77-97. `resolveOpencodeConfig()` (99-107) выбирает ensurer, если он не-nil, иначе `ReadConfig()`. `newOpencodePort()` (118-129) вызывает `resolveOpencodeConfig`, строит клиент и на строке 128 ставит `SetServerEnsurer(serverEnsurerFunc(opencode.EnsureServer))`.

Первый вызов `newOpencodePort()` при старте процесса видит ensurer равным nil и читает только `service.json`. К моменту второго вызова ensurer уже установлен. Второй вызов приходит из `cmd/beacon/mcp.go:1517` через `opencodeWakePorts()` при сборке wake-реестра. Если `service.json` к этому моменту исчез, `EnsureServer` спавнит новый процесс. Эта цепочка описана в `docs/research/mcp-q1-startup.md:51-59` и `docs/research/mcp-spawn-rootcause.md:7-24`.

Другие точки входа в один и тот же конструктор: `cmd/beacon/main.go:132`, `cmd/beacon/registry_sync.go:295`, `cmd/beacon/mcp.go:1517`. Три вызова приходятся на старт CLI, путь MCP и команду registry sync.

## 2. CLI `opencode api`: возможности и контракт

### Что показал бинарник

Версия `opencode v2.3.2`. Вывод `opencode api --help`:

```text
DESCRIPTION
  Make a request to the running server

USAGE
  opencode api [flags] <operation | method path...>

ARGUMENTS
  operation | method path... string OpenAPI operation ID, or an HTTP method followed by a path

FLAGS
  --data, -d string      Request body
  --header, -H string    Request header in name:value form
  --param key=value      OpenAPI path or query parameter

GLOBAL FLAGS
  --standalone     Run with a private server instead of the background service
  --server string  Connect to a server URL instead of the background service
```

`opencode --help` показывает отдельный подраздел `service` с командами `start`, `restart`, `status`, `stop`, `get`, `set`, `unset`. `opencode serve` имеет флаги `--hostname`, `--port`, `--cors`, `--service`, `--stdio`.

### discovery и авторизация

`opencode api session.list` без флагов и без переменных окружения вернул `rc=0` и 27703 байта JSON. Фоновая служба уже была зарегистрирована в `/home/nbook/.local/state/opencode/service.json`. Команда прочитала адрес и пароль из файла сама. Флаг `--server` переопределяет адрес, `OPENCODE_PASSWORD` переопределяет пароль. Это подтверждает `tests/e2e/e2e_test.go:364-384`, где изолированный сервер вызывается как `opencode api --server <url> -d <data> <method> <path>` с `OPENCODE_PASSWORD` в окружении.

Формат вывода. Операционные ID вроде `session.list` возвращают `{"data":[...]}`. Сырой метод и путь возвращают конверт `{"data":..., "location":{...}}`, который разбирает `apiResp` в e2e-тесте. Тело запроса передаётся флагом `--data` / `-d`, а не через stdin. stdout содержит JSON, stderr содержит ошибки и логи, код выхода 0 при успехе и ненулевой при ошибке. Таймаут задаётся вызывающей стороной через `context.WithTimeout` или `exec.CommandContext`.

### Может ли `opencode api` поднять сервис сам

Да, может. Первичный источник из `packages/core/src/plugin/skill/opencode.md`, процитированный в `docs/research/opencode-experimental-wake-api.md:42`, говорит:

> It uses the same discovery and authentication flow as the TUI and may start the background service when no compatible healthy service is available.

Локальный замер с пустым `XDG_STATE_HOME` это подтвердил. `XDG_STATE_HOME=<tmp> timeout 20 opencode api session.list` завершился по таймауту (`rc=124`) без вывода. В каталоге `<tmp>` не появилось ни одного файла, включая `opencode/service.json`. Зато во время выхода появился процесс `/home/nbook/.bun/install/global/node_modules/@opencode/cli/bin/opencode.exe serve --service` с `PPID=1`. Он не записал registration в указанный каталог. Процесс был завершён после замера, альтернативный каталог удалён.

Вывод из замера. `opencode api` при отсутствии здоровой регистрации действительно спавнит демона, но это демон OpenCode, а не ребёнок Beacon. Владелец процесса OpenCode, родитель становится `init` (PPID 1), жизненный цикл и файл `service.json` управляются со стороны OpenCode. Beacon не задаёт порт через `net.Listen(:0)`, не генерирует пароль, не пишет `opencode-serve.json` и не ставит `Pdeathsig`. При этом поведение не детерминировано: в изолированном каталоге демон не зарегистрировался, а сама команда зависла. Полагаться на неявный автозапуск как на механизм discovery нельзя.

`--standalone` не подходит как замена. Он поднимает приватный сервер на один вызов. Такой сервер не содержит сессий агента, поэтому `deliver` не сможет доставить вердикт, а `watch --live` потеряет поток после завершения команды.

SSE остаётся исключением. `opencode api` не отдаёт тело `/api/event`. `docs/adrs/0061-local-e2e-with-live-opencode.md:23-28` фиксирует `Operation not found`, а `tests/e2e/e2e_test.go:482-485` фиксирует ноль байт. Для живого потока нужен прямой HTTP-читатель по адресу из `service.json`. Он читает только адрес и не спавнит процесс.

## 3. Целевая конфигурация без EnsureServer

Инвариант: ни один путь в `cmd/beacon` и `internal/infra/opencode` не вызывает `EnsureServer`, `startServeProcess`, `startServer`, `freePort`, `waitHealthy` и не пишет `opencode-serve.json`. Владение процессом полностью принадлежит OpenCode.

Discovery остаётся единственным источником адреса для HTTP-клиента и SSE. Это `ReadConfig()` из `internal/infra/opencode/client.go`, читающий `service.json`. Значение `url` и `password` передаётся в `opencode.New()`. Никакой fallback-стартер не устанавливается.

Решение для случая "сервиса нет": **fail fast на слое discovery, плюс существующий CLI-путь как фолбэк, управляемый OpenCode.**

Слой discovery (`newOpencodePort` и все его вызывающие) при отсутствии или битом `service.json` возвращает ошибку. `wireOpenCodePorts` не устанавливает порты, команды печатают честное сообщение об отсутствии регистрации. Beacon не вызывает `opencode serve`, не вызывает `opencode service start` изнутри `resolveOpencodeConfig` и не создаёт свой файл состояния. Это даёт предсказуемое поведение и позволяет проверить инвариант поиском по тексту.

Слой CLI сохраняет вызовы `opencode api`. Они уже есть в `watch`, `agents`, `wake/deliverCLI` и `T202API`. Если сервис отсутствует, OpenCode может поднять свой демон по собственной политике. Beacon этим процессом не управляет и не считает его своим. Такое поведение допустимо, но его нужно явно отличать от спавна Beacon. Для команд `watch`, `agents` и `registry sync` можно добавить общую проверку наличия `service.json` перед вызовом CLI, чтобы не провоцировать неявный автозапуск. Это опциональный шаг после основного patch.

Отклонённые варианты. Ленивый ensurer внутри `PromptFunc` сохраняет спавн и только откладывает его. `--standalone` создаёт сервер без чужих сессий. Явный `opencode service start` внутри `resolveOpencodeConfig` убирает спавн Beacon как владельца, но добавляет третий механизм в горячий путь и размывает границу "только discovery". Если владелец захочет автоматический bootstrap, его нужно вынести в отдельную команду `beacon doctor --fix` или в действия оператора, а не в `resolveOpencodeConfig`.

## 4. Влияние на точки

### `cmd/beacon/mcp.go:1517`

`opencodeWakePorts()` вызывает `newOpencodePort()`, строит `PromptFunc` и `SessionActiveFunc` из `*opencode.Client`. После удаления ensurer второй вызов перестаёт спавнить и читает только `service.json`. При ошибке функция по-прежнему возвращает `nil, nil`, и `OpenCodeDeliverer` уходит в `deliverCLI`, то есть в `opencode api`. Доступ к `EnsureServer` из wake-цепочки исчезает. Для явности можно переименовать конструктор в `newReadOnlyOpencodePort()`, как предложено в `docs/research/mcp-lazy-port-design.md:90-112`. После полного удаления ensurer переименование не меняет поведения.

### `cmd/beacon/main.go:132`

`wireOpenCodePorts()` вызывает `newOpencodePort()` один раз при старте любого CLI-процесса. После правки он по-прежнему читает `service.json`, но не устанавливает fallback-стартер. Ветка `if ensurer, ok := port.(serverEnsurer)` в `installOpenCodePorts()` (156-158) становится мёртвой и удаляется. Отсутствие `service.json` остаётся не фатальным на старте, как и сейчас. Порты `liveSource`, `sessionSpawner`, `deliveryTarget` и `sessionCreator` продолжают устанавливаться при наличии конфига.

### `cmd/beacon/registry_sync.go:295`

`registrySyncOpenSource()` вызывает `newOpencodePort()` и возвращает `registrySessionSource`. После правки это read-only discovery. При отсутствии `service.json` команда `beacon registry sync` получает ошибку и печатает её, не поднимая сервер. Спавн из reconcile-пути исчезает.

### CLI `spawn`, `deliver`, `watch --live`

`beacon spawn` берёт `spawnSessionSpawner`, который ставится через `SetSessionCreator` из того же клиента. Клиент строится по `service.json`. Сервер больше не спавнится. При отсутствии регистрации порт остаётся nil, и команда сообщает об ошибке. При желании запись можно перевести на `opencode api POST /api/session` через готовый `T202API` из `internal/infra/wake/deliver.go:842-906`.

`beacon deliver` берёт `deliveryTarget` через `SetDeliveryTarget`. Поведение то же: discovery, никакого спавна, честный unsupported при отсутствии порта. Wake-доставка внутри MCP остаётся на `deliverCLI`.

`beacon watch` без `--live` вызывает `opencode api session.list` напрямую в `watch.go:238` и не проходит через `newOpencodePort`. Спавна Beacon в этом пути нет. Единственный риск это неявный автозапуск демона со стороны OpenCode при отсутствии сервиса.

`beacon watch --live` получает `liveSource` из `installOpenCodePorts` и читает SSE через `GET /api/event` прямым HTTP-клиентом. После правки клиент строится только discovery. SSE-читатель остаётся, поскольку CLI не отдаёт тело потока. При отсутствии `service.json` живой источник не устанавливается, и команда завершается честно.

### Дополнительные точки

`cmd/beacon/agents.go:129-164` и `cmd/beacon/attach.go:44-74` читают `service.json` локальными копиями. `cmd/beacon/doctor.go:297-306` считает базовый каталог состояния. Они не спавнят процесс, но дублируют discovery. Объединение их в одну функцию `Discover()` описано в `opencode-adapter-audit.md:134` и `:185`. Это отдельный шаг, не обязательный для инварианта "без спавна".

`cmd/beacon/gc.go:28-37` подключён к шести функциям из `server.go`. После удаления writer'а `WriteServeState` файл `opencode-serve.json` перестаёт создаваться. Защита по PID и порту из `service.json` остаётся рабочей, как отмечено в `docs/research/process-leak-mechanism.md:54`. Ветку про запись Beacon нужно убрать или оставить как миграцию на один релиз для чистки старых записей. `ListServeProcesses` и `TerminateServe` можно оставить как общий GC чужих процессов либо заменить на `opencode service status` и `opencode service stop`. Это решение владельца.

## 5. Patch-план и критерии проверки

План разбит на шаги с проверяемым состоянием после каждого. Порядок сохраняет один инвариант: сначала убирается путь вызова, потом реализация.

**Шаг 0. Baseline.** Записать текущий результат `rg -n 'EnsureServer|startServeProcess|SetServerEnsurer' cmd/beacon internal/infra/opencode` и текущий PID из `service.json`. Без числа до правки нечем доказать исчезновение вызовов.

**Шаг 1. Убрать ensurer из cmd/beacon.** В `cmd/beacon/opencode.go` удалить `serverEnsurer`, `serverEnsurerFunc`, `installedServerEnsurer`, `SetServerEnsurer`, ветку ensurer в `resolveOpencodeConfig` и строку 128. `resolveOpencodeConfig` становится тонкой обёрткой над `opencode.ReadConfig()`. В `cmd/beacon/main.go` удалить ветку `serverEnsurer` из `installOpenCodePorts`. После шага ни один файл в `cmd/beacon` не содержит имени `EnsureServer`. Обновить ссылки в `opencode_test.go` и `main_test.go`, которые устанавливают или считают ensurer.

**Шаг 2. Убрать спавн из internal/infra/opencode/server.go.** Удалить константы и типы спавна 26-143, функции `healthReady` и `waitHealthy` 154-191, `ServeState` и функции работы с `opencode-serve.json` 193-281, функцию `EnsureServer` 387-429. Сохранить `randomPassword()` 145-152, поскольку она используется `ScopeTokenStore.Issue` в строке 537. Сохранить `stateHomeDir`, `serviceRegistrationPath`, `ReadServicePID`, `ProcessAlive`, `TerminateServe`, `ServeProcess`, `ListServeProcesses`, `parseServeCmdline` и весь `ScopeTokenStore`. Убрать из imports `os/exec`, `net`, `syscall` и `strings`, если после удаления они не используются.

**Шаг 3. Привести GC к новому состоянию.** Убрать защиту записи Beacon из `gcServePlan()` в `cmd/beacon/gc.go:53-95` и очистку `gcServeClear` из `runGCWithServe`. Оставить защиту PID и порта из `service.json`. Если владелец хочет миграцию, оставить `ReadServeState` и `ClearServeState` как read-only утилиты на один релиз, но без writer'а и без создания новых файлов.

**Шаг 4. Выровнять CLI-команды.** Добавить общую проверку наличия `service.json` перед вызовами `opencode api` в `watch.go:238`, `agents.go:214` и перед `registrySyncOpenSource`. При отсутствии файла команда печатает то же сообщение, что и discovery-слой, и не дёргает CLI. Этот шаг опциональный и не влияет на инвариант шага 1.

**Шаг 5. Объединить discovery в одну функцию.** Создать `Discover()` в `internal/infra/opencode` и перевести `agents.go`, `attach.go`, `doctor.go`, `gc.go` и `client.go` на неё. Удалить локальные `opencodeServiceFile`, `serviceConfigPath` и `stateBaseDir`. Шаг увеличивает diff и делается после стабилизации шагов 1-3.

Критерии проверки без тестов.

1. `rg -n 'EnsureServer|startServeProcess|startServer|freePort|waitHealthy|spawnRequest|serveStateFile|WriteServeState' cmd/beacon internal/infra/opencode` возвращает ноль совпадений в продакшн-коде. Допустимы только упоминания в тестах до их обновления.
2. `rg -n 'SetServerEnsurer|serverEnsurer|installedServerEnsurer' cmd/beacon` возвращает ноль совпадений.
3. `rg -n 'opencode-serve.json' cmd/beacon internal/infra/opencode docs` показывает только исторические упоминания в research-документах и миграционные заметки.
4. `rg -n 'newOpencodePort\(\)' cmd/beacon` показывает три вызова в `main.go:132`, `mcp.go:1517` и `registry_sync.go:295`. Каждый из них разрешается через `opencode.ReadConfig()`, что проверяется чтением `resolveOpencodeConfig`.
5. В `internal/infra/opencode/server.go` отсутствуют импорты `os/exec` и `net`, а `ScopeTokenStore` и `randomPassword` остаются на месте.
6. Наблюдение за процессом при живом `service.json`. До и после запуска `beacon registry sync`, `beacon watch --live` и `beacon mcp` PID в `service.json` не меняется. В `ps` не появляется процесс вида `opencode serve --hostname 127.0.0.1 --port <случайный>`, чей родитель это beacon. Команда `ps -eo pid,ppid,args | grep '[s]erve'` даёт только штатный демон с PID из registration-файла.
7. Наблюдение в изолированном окружении. С поддельным `XDG_STATE_HOME` без `service.json` команды из пункта 6 печатают ошибку об отсутствии регистрации. Файл `opencode-serve.json` не создаётся. Процесс, чей родитель beacon, не появляется. Если вызывается `opencode api`, допустим только демон OpenCode с PPID 1, и это фиксируется отдельной строкой отчёта.
8. `gofmt -w` применён только к изменённым `.go`-файлам. `git diff --stat` показывает только файлы из шагов 1-5. `go test`, `go build` и `beacon gates` не запускаются локально по правилам AGENTS.md.

Риск шага 1 в том, что `opencode.Client` не реализует `serverEnsurer`, поэтому ветка в `installOpenCodePorts` в prod и так ничего не ставила. Удаление строки 128 в `newOpencodePort` прекращает спавн для всех команд сразу, а не только для MCP. Это совпадает с выводом `docs/research/mcp-q4-target.md:105` и `:170`.

## Источники

Локальные файлы: `cmd/beacon/opencode.go`, `cmd/beacon/main.go`, `cmd/beacon/mcp.go`, `cmd/beacon/registry_sync.go`, `cmd/beacon/watch.go`, `cmd/beacon/spawn.go`, `cmd/beacon/deliver.go`, `cmd/beacon/gc.go`, `cmd/beacon/agents.go`, `cmd/beacon/attach.go`, `internal/infra/opencode/client.go`, `internal/infra/opencode/server.go`, `internal/infra/wake/deliver.go`, `tests/e2e/e2e_test.go`.

Документы: `docs/adrs/0060-cli-adapter-boundary-and-live-loop.md`, `docs/adrs/0061-local-e2e-with-live-opencode.md`, `docs/adrs/0092-background-server-source.md`, `docs/adrs/0095-cli-opencode-api.md`, `docs/adrs/0194-cli-opencode-api.md`, `docs/research/opencode-adapter-audit.md`, `docs/research/opencode-experimental-wake-api.md`, `docs/research/mcp-lazy-port-design.md`, `docs/research/mcp-q1-startup.md`, `docs/research/mcp-q4-target.md`, `docs/research/mcp-spawn-rootcause.md`, `docs/research/process-leak-mechanism.md`.

Замер CLI выполнен локальным бинарником `opencode v2.3.2` из `/home/nbook/.bun/bin/opencode`. Первичная формулировка про автозапуск взята из `docs/research/opencode-experimental-wake-api.md:42`, `:91`.

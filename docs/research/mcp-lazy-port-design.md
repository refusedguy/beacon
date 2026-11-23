# Проект правки: mcp не создаёт серверы opencode

Дата: 2026-11-21. Режим только чтение. Код, конфиги и тесты не менялись. `go test`, `go build`, `beacon gates` и `tools.beacon.*` не запускались. Поиск шёл через `rg` с явными каталогами `cmd/`, `internal/`, `.opencode/`, `tests/`.

Ветка main: `2a3362f1`.

## Задача

`beacon mcp` поднимается как локальный stdio-процесс на каждый вызов `tools.beacon.*`. Владелец хочет, чтобы mcp работал без создания новых серверов `opencode serve`. Нужно спроектировать минимальную правку и сравнить три варианта.

## Где opencodePort используется в mcp

Девять MCP-инструментов не используют opencodePort напрямую. `buildMCPDependencies` в `cmd/beacon/mcp.go:542-591` собирает зависимости только из SQLite: `Store`, `Stamper`, `Writer`, `Events`, `Source`, `Snapshot` и остальные порты работают на одной DB-ручке. HTTP-клиент нужен фоновому реконсилятору, который внутри того же процесса доставляет wake в живые сессии.

Полный путь от старта процесса до вызова порта:

1. `cmd/beacon/main.go:121` безусловно вызывает `wireOpenCodePorts()`.
2. `cmd/beacon/main.go:131-137`: `wireOpenCodePorts()` вызывает `newOpencodePort()`, затем `installOpenCodePorts(port)`.
3. `cmd/beacon/opencode.go:118-129`: `newOpencodePort()` вызывает `resolveOpencodeConfig()` (строка 119), затем ставит глобальные порты `SetSessionCreator` (127) и `SetServerEnsurer` (128).
4. `cmd/beacon/opencode.go:102-107`: `resolveOpencodeConfig()` выбирает `installedServerEnsurer.EnsureServer(ctx)`, если он установлен, иначе `opencode.ReadConfig()`.
5. `cmd/beacon/mcp.go:1127 runMCP` строит DB-зависимости (1190) и назначает `cfg.onPollerOwner` (1214-1217).
6. `cfg.onPollerOwner` вызывает `startHTTPAPI` (894), тот в конструкции `Sink` вызывает `reconcileWaker(cfg.db)` (906).
7. `reconcileWaker` (1547-1556) вызывает `opencodeWakePorts()` (1549).
8. `opencodeWakePorts` (1516-1542) вызывает `newOpencodePort()` (1517). Это та самая строка из условия задачи.

Реконсилятор вызывает `reconcileWaker` ещё несколько раз: `makeEscalator` при каждом проходе (`mcp.go:1586`), повторная отправка ladder pointer (1664) и `WithWaker` в `ReconcilePass` (1732). Все эти вызовы идут через одну и ту же функцию 1517, поэтому достаточно правки в одном месте.

Остальные потребители порта лежат вне mcp-инструментов и используют глобальные порты, которые ставит `main.go`:

- session spawn: `cmd/beacon/spawn.go:43 SetSessionSpawner`, вызывается командой `beacon spawn`;
- wake delivery CLI: `cmd/beacon/deliver.go:457` и `cmd/beacon/orchestrator.go:227` через `deliverTarget`;
- live watch: `cmd/beacon/watch.go:314 SetLiveSource`, вызывается `beacon watch --live`;
- registry sync: `cmd/beacon/registry_sync.go:294-296`, отдельный seam `registrySyncOpenSource`, который тоже вызывает `newOpencodePort()`.

Ни один из этих вызовов не идет из обработчика `beacon_watch_pr`, `beacon_wait_result`, `beacon_get_result`, `beacon_list_running`, `beacon_ack`, `beacon_cancel_watch`, `beacon_stats`, `beacon_defer_wiring` или `beacon_task_claim`.

### Уточнение про условие спавна

`EnsureServer` (`internal/infra/opencode/server.go:390-429`) спавнит сервер только если нет живого PID в `opencode-serve.json` и нет `service.json`. На пути mcp ensurer становится не-nil только после успешного `newOpencodePort()` в `wireOpenCodePorts()`. Если `service.json` нет в оба момента, вызов на строке 1517 возвращает ошибку, порты wake остаются nil, и доставка уходит в CLI-путь `internal/infra/wake/deliver.go:310-343`. Если `service.json` есть, `EnsureServer` возвращает его конфиг без спавна. Значит спавн на старте mcp возможен в окне, когда ensurer уже установлен, а registration-файл пропал. Такое окно было в наблюдаемом инциденте. Правка должна убрать саму зависимость от ensurer, а не пытаться угадать состояние файла.

## Можно ли сделать инициализацию ленивой

Да, технически можно. Но у точной формулировки есть важное уточнение: порт не нужен ни одному MCP-инструменту. Он нужен реконсилятору в момент доставки wake. Ленивая инициализация означает перенос `newOpencodePort()` из `reconcileWaker` в тело замыканий `prompt` и `active` с кешированием через `sync.Once`.

Ленивость сама по себе не запрещает спавн. При первой доставке wake вызов `newOpencodePort()` все равно пойдет через `resolveOpencodeConfig()` и может вызвать `EnsureServer`. Поэтому ленивая инициализация решает задачу владельца только в связке с чтением конфига без fallback сервера.

Есть и вторая причина не делать ленивость главной правкой. `wake.OpenCodeDeliverer` различает nil-порты и порты с ошибкой (`internal/infra/wake/deliver.go:297-313`). Если замыкания существуют, но конфига нет, `active` обязан вернуть `(false, true, nil)`, иначе `Deliver` решит, что сессия мертва (строки 263-271), и не дойдет до CLI-фолбэка. При текущем поведении `opencodeWakePorts()` возвращает nil, nil, и deliverer вообще не вызывает SessionActiveFunc. Ленивый вариант меняет эту деталь и требует отдельного теста.

## Сравнение вариантов

### Вариант A: убрать fallback в mcp, оставить ReadConfig-only

Что меняется. В `cmd/beacon/opencode.go` добавляется конструктор, который читает только `opencode.ReadConfig()` и не трогает глобальный ensurer. `cmd/beacon/mcp.go:1517` вызывает его вместо `newOpencodePort()`. Глобальная проводка в `main.go` и сам `newOpencodePort()` остаются как есть, поэтому CLI-команды сохраняют T3.151 поведение.

Риск низкий. При отсутствии `service.json` wake уходит в `opencode api` вместо HTTP. Это существующий фолбэк, он не создает сервер. Теряется возможность доставить wake через сервер, который Beacon ранее спавнил и записал в `opencode-serve.json`, если пользовательского `service.json` нет. Для требования владельца это ожидаемый компромисс.

Заденет тесты. Существующие тесты `cmd/beacon/opencode_test.go:170-217`, `cmd/beacon/opencode_test.go:324-372`, `cmd/beacon/main_test.go:737-762` и `cmd/beacon/main_test.go:778-814` проверяют `newOpencodePort`, `resolveOpencodeConfig` и `installOpenCodePorts`. Если не менять эти функции, правки в них не нужны. Требуется один новый тест на read-only конструктор.

### Вариант B: ленивая инициализация в mcp.go

Что меняется. `opencodeWakePorts` (1516-1542) перестает вызывать `newOpencodePort()` при построении registry. Внутри `prompt` и `active` появляется `sync.Once`, который при первом вызове создает клиента и кеширует его. Точка resolve переносится со старта mcp на первую доставку wake.

Риск средний. Нужно сохранить семантику nil-портов, иначе первая доставка при отсутствии конфига завершится ошибкой вместо CLI-фолбэка. Нужно решить, делится ли `sync.Once` между registry. Локальный `once` на вызов `opencodeWakePorts` проще и не требует сброса в тестах. Общий пакетный `once` экономит несколько чтений файла, но добавляет глобальное состояние, которое тесты обязаны сбрасывать.

Заденет тесты. Прямых тестов `opencodeWakePorts` и `reconcileWaker` в `cmd/beacon` нет. Для варианта B нужен seam вместо прямого вызова конструктора и новый тест, который проверяет, что до первого вызова `prompt` или `active` конструктор не вызывался.

Вариант B сам по себе не гарантирует отсутствие спавна при первой доставке.

### Вариант C: не поднимать mcp как stdio на каждый вызов

Что меняется. `.opencode/plugins/beacon-project/mcp.js:9-20` регистрирует локальный сервер с командой `[".opencode/bin/beacon", "mcp"]`. Вместо этого можно включить remote-режим: `cmd/beacon/mcp.go:464-472` читает `BEACON_MCP_REMOTE`, а ветка `1169-1178` возвращает `serveMCPRemote`, не открывая DB, не запуская реконсилятор и не создавая opencodePort. Технически процесс `beacon mcp` все равно стартует на вызов, но становится тонким HTTP-клиентом.

Риск высокий. Remote-режим требует работающего сервера с endpoint `/mcp` и файла `connection.json`. Комментарий в `mcp.go:460-463` прямо говорит, что endpoint приходит с BS.43, то есть инфраструктура еще не готова. Правка плагина сломает адаптер-тест `tests/adapter/beacon-project-mcp.test.js:52-64`, который ждет `type: "local"` и массив `command`. Вариант не лечит прямой запуск `beacon mcp` вне плагина.

Заденет тесты. `tests/adapter/beacon-project-mcp.test.js:52-64` и, при включении remote-переменной в конфиге сессии, Go-тесты remote-ветки в `cmd/beacon/mcp_test.go`.

## Проверка исправления PR 887 в main

Коммит `432a77e7 fix(opencode): reuse live serve and gc orphan processes (Q-A-02) (#887)` присутствует в истории ветки. Все четыре элемента в working tree на `2a3362f1`:

- Pdeathsig: `internal/infra/opencode/server.go:118`, `cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}`.
- PID-state: `serveStateFile = "opencode-serve.json"` в `server.go:43`, структура `ServeState` с полем `PID` в `197-201`, проверка `ReadServeState()` и `ProcessAlive(st.PID)` в `394`, запись состояния в `424`.
- healthReady и waitHealthy: `server.go:154-191`, вызов `waitHealthy(..., proc.Done())` в `415`, при ошибке процесс убивается (416).
- ensureMu: `server.go:203-205`, блокировка на входе `EnsureServer` в `391-392`.

Дополнительно `beacon gc` подключен к dispatch в `cmd/beacon/main.go:274`, а `runGC` лежит в `cmd/beacon/watch.go:819-821`.

Вывод: повторно чинить эти четыре элемента не нужно. Предлагаемая правка не трогает `internal/infra/opencode/server.go` и не ослабляет защиту от сирот.

## Рекомендация

Приоритет 1, обязательная часть: вариант A. Он один гарантирует, что mcp никогда не вызовет `EnsureServer`, и требует две точечные правки.

Файл `cmd/beacon/opencode.go`, сразу после `newOpencodePort()` (после строки 129). Добавить:

```go
// newReadOnlyOpencodePort builds a client from an existing registration only.
// It never installs the fallback server starter, so the MCP wake path cannot
// spawn opencode serve. A missing registration leaves the wake ports nil and
// the deliverer uses its own CLI path.
func newReadOnlyOpencodePort() (opencodePort, error) {
	cfg, err := opencode.ReadConfig()
	if err != nil {
		return nil, err
	}
	return opencode.New(cfg, opencodeHTTPDoer)
}
```

Файл `cmd/beacon/mcp.go`, строка 1517 внутри `opencodeWakePorts()`. Заменить `newOpencodePort()` на `newReadOnlyOpencodePort()`. Остаток функции, включая возврат `nil, nil` при ошибке, оставить без изменений. Тем самым сохранится текущая семантика deliverer: нет конфига, значит нет PromptFunc и SessionActiveFunc, значит CLI-фолбэк.

Приоритет 2, опционально: вариант B поверх A. Если владелец хочет, чтобы mcp не читал `service.json` и не строил клиента на старте, перенести вызов `newReadOnlyOpencodePort()` в замыкания `prompt` и `active` с локальным `sync.Once`. В `active` при отсутствии клиента возвращать `(false, true, nil)`, чтобы `Deliver` дошел до `PromptFunc`, а тот вернет `wake.ErrNoPromptPort`. Это меняет `resume` на true по сравнению с текущим nil-nil поведением, поэтому правку нужно покрыть тестом. Для требования «без новых серверов» этот шаг не нужен.

Приоритет 3, не принимать сейчас: вариант C. Он зависит от BS.43 и ломает адаптер-тест плагина. Вернуться к нему после появления рабочего `/mcp` на сервере.

## Как проверить без go test и go build

Проверка текстом и логикой, в таком порядке:

1. `rg -n "newOpencodePort|newReadOnlyOpencodePort|resolveOpencodeConfig|EnsureServer|reconcileWaker|opencodeWakePorts" cmd/beacon internal/infra/opencode` после правки. Ожидаемый результат: в `cmd/beacon/mcp.go` нет вызовов `newOpencodePort`, `resolveOpencodeConfig` и `EnsureServer`, а строка 1517 ссылается на `newReadOnlyOpencodePort`. Вызов `EnsureServer` в `cmd/beacon` отсутствует полностью, если не считать адаптер `serverEnsurerFunc` и тесты.
2. Прочитать новый `newReadOnlyOpencodePort()` и убедиться, что внутри только `opencode.ReadConfig()` и `opencode.New()`, без `SetServerEnsurer` и без `resolveOpencodeConfig`.
3. Проследить цепочку 1516 - 1549 - 906 - 1214 вручную. Убедиться, что все четыре вызова `reconcileWaker` теперь приходят к read-only конструктору.
4. Посмотреть `git diff -- cmd/beacon/mcp.go cmd/beacon/opencode.go`. Ожидаемый размер: одна новая функция около 10 строк и одна замена имени функции. Изменений в `internal/infra/opencode/server.go`, `main.go` и плагине нет.
5. Сверить тест-инвентарь: перечисленные выше тесты `opencode_test.go` и `main_test.go` остаются без правок. Добавить в `cmd/beacon/opencode_test.go` тест со считающим ensurerом. При отсутствии `service.json` read-only конструктор возвращает ошибку, вызовов ensurer ноль, файл `opencode-serve.json` не появляется. При наличии `service.json` конструктор возвращает клиент, вызовов ensurer тоже ноль.
6. Дополнительная наблюдательная проверка без запуска тестов: сравнить число процессов `opencode serve --hostname 127.0.0.1` до и после нескольких вызовов `tools.beacon.*`. После правки число не растет. Эта проверка требует работающего окружения и выполняется владельцем, а не сабагентом в режиме только чтение.

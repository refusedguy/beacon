# Почему beacon mcp спавнит opencode serve

Проверка от 2026-11-21. Репозиторий `/mnt/projects/beacon-handoff/beacon-impl`, HEAD `2a3362f1`. Код не менялся, тесты и сборки не запускались, инструменты beacon не вызывались.

## Краткий ответ

`newOpencodePort()` вызывается дважды при старте `beacon mcp`. Первый вызов идёт из `main()` и только читает `service.json`, после чего ставит fallback-стартер. Второй вызов приходит из wake-адаптера фонового reconciler'а. К этому моменту fallback уже установлен, поэтому `resolveOpencodeConfig()` вызывает `EnsureServer()`. Если нет живого `opencode-serve.json` и нет `service.json`, `EnsureServer()` спавнит новый `opencode serve` на свободном порту.

Спавн не помогает доставке wake. Новый сервер не содержит сессий агента. Доставка работает через `service.json` того демона, в котором живёт сессия.

## Цепочка вызовов

1. `cmd/beacon/main.go:116-125` вызывает `wireOpenCodePorts()` безусловно для любой команды.
2. `cmd/beacon/main.go:131-137` вызывает `newOpencodePort()`.
3. `cmd/beacon/opencode.go:118-130` вызывает `resolveOpencodeConfig()`. `installedServerEnsurer` ещё nil, поэтому `cmd/beacon/opencode.go:102-107` возвращает `opencode.ReadConfig()`. Спавна нет. Строка 128 ставит `SetServerEnsurer(serverEnsurerFunc(opencode.EnsureServer))`.
4. `cmd/beacon/main.go:263` передаёт управление `mcpRunner`, а `cmd/beacon/mcp.go:392-398` вызывает `runMCP`.
5. `cmd/beacon/mcp.go:1127-1243` вызывает `runReconciler` на строке 1220.
6. `cmd/beacon/mcp.go:1787-1810` вызывает `onPollerOwner` на строке 1797, только если процесс держит poller lock.
7. `cmd/beacon/mcp.go:1214-1217` вызывает `startHTTPAPI`.
8. `cmd/beacon/mcp.go:894-918` создаёт `humanDecisionSink` с `registry: reconcileWaker(cfg.db)` на строке 906.
9. `cmd/beacon/mcp.go:1547-1556` вызывает `opencodeWakePorts` на строке 1549.
10. `cmd/beacon/mcp.go:1516-1542` вызывает `newOpencodePort()` на строке 1517. Это второй вызов.
11. `cmd/beacon/opencode.go:119` видит установленный ensurer, и `cmd/beacon/opencode.go:104` вызывает `EnsureServer()`.
12. `internal/infra/opencode/server.go:390-429` сначала читает `ReadServeState()` и проверяет `ProcessAlive()` на строке 394, затем `ReadConfig()` на строке 397, затем вызывает `freePort`, `startServer`, `waitHealthy`, `WriteServeState` на строках 401-424.
13. `internal/infra/opencode/server.go:101-128` запускает `opencode serve --hostname 127.0.0.1 --port <free>`.

Другие вызовы `reconcileWaker` в том же процессе находятся в `cmd/beacon/mcp.go:1586`, `:1664` и `:1732`. Они работают в фоновом цикле и тоже проходят через `opencodeWakePorts`.

Условие срабатывания. Второй вызов на старте происходит у процесса, который выиграл poller lock. Если лок принадлежит другому процессу, `onPollerOwner` не вызывается и стартового спавна нет. Первый вызов из `main()` происходит в любом случае.

## Кто реально использует opencodePort

Инструменты MCP не вызывают `newOpencodePort`. `mcp.Dependencies` не содержит порта wake или delivery (`internal/infra/mcp/server.go:396-459`). Инструменты работают через store, stamper, writer и snapshot.

Wake доставляет фоновый reconciler через `reconcileWaker`. Это путь слияния `cibridge` в `cmd/beacon/mcp.go:1731-1734`, лестница в `:1586` и `:1664`, решения из HTTP API в `:906`. Инструмент `beacon_ack` с `approve` или `reject` меняет состояние Watch в store, а указатель уходит позже из цикла reconciler'а.

Порт из `newOpencodePort()` используют CLI-команды `spawn`, `deliver`, `watch --live` и `registry sync`. Их wiring находится в `cmd/beacon/main.go:145-159`.

## Минимальный fix

В `cmd/beacon/mcp.go:1516-1520` заменить вызов `newOpencodePort()` на чтение только registration-файла:

```go
func opencodeWakePorts() (wake.PromptFunc, wake.SessionActiveFunc) {
	cfg, err := opencode.ReadConfig()
	if err != nil {
		return nil, nil
	}
	client, err := opencode.New(cfg, opencodeHTTPDoer)
	if err != nil {
		return nil, nil
	}
	// Далее без изменений, строки 1521-1541.
}
```

Пакет `opencode` уже импортирован в `mcp.go`, он используется на строке 1521. Переменная `opencodeHTTPDoer` определена в `cmd/beacon/opencode.go:111`. Правка затрагивает один файл и одну функцию.

Почему это безопасно.

Комментарий в `cmd/beacon/mcp.go:1513-1515` уже обещает именно такое поведение: нет registration, нет портов.

При наличии `service.json` HTTP-доставка работает как раньше.

При отсутствии `service.json` `OpenCodeDeliverer` получает nil-порты и переходит на CLI-путь. См. `internal/infra/wake/deliver.go:208-211`, `:262` и `:297`. Обязательство остаётся недоставленным и может быть повторено.

Свежий сервер не может доставить wake в сессию агента. В нём нет сессий, и `SessionActiveFunc` из `cmd/beacon/mcp.go:1528-1540` всё равно ответил бы `ErrSessionDead`.

Альтернативы, которые не стоит брать. Обнулять `installedServerEnsurer` перед вызовом или не ставить его в `newOpencodePort`. Это убирает fallback у `beacon spawn` и `beacon deliver` и ломает T3.151. Ленивый порт с вызовом `EnsureServer()` внутри `PromptFunc` сохраняет спавн и только откладывает его, поэтому проблему не решает.

Риск правки. В окружении без `service.json`, но с работающим демоном opencode, который не пишет registration, HTTP-доставка исчезнет. Такой демон недоступен и текущему коду, потому что стартовый `newOpencodePort()` тоже читает только `ReadConfig()`. Реальной потерей это не будет.

## Что уже влито из PR 887 и Q-A-02

Все три элемента присутствуют в `2a3362f1`.

Pdeathsig находится в `internal/infra/opencode/server.go:118`, строка `cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}`.

PID-state находится в `ServeState.PID` на `:197-201`, в `WriteServeState` на `:253-269`, в проверке `ProcessAlive(st.PID)` перед переиспользованием на `:394` и в записи PID на `:424`.

Health check находится в `healthPath` и таймаутах на `:31-39`, в `healthReady` на `:154-167`, в `waitHealthy` на `:171-191` и в вызове на `:415`.

Dispatch `case "gc"` есть в `cmd/beacon/main.go:274`.

Не влита catalog-запись для `beacon gc`. См. `docs/tasks/Q-A-02.md:22`.

PR 887 это коммит `432a77e7` от 2026-11-21 с заголовком `fix(opencode): reuse live serve and gc orphan processes (Q-A-02) (#887)`. Он сделал переиспользование безопасным, но не убрал вызов `EnsureServer()` при старте mcp. Сам спавн появился раньше. Fallback в `EnsureServer` пришёл с T3.151 (`docs/tasks/T3.151.md:16-17`, PR 250). Установку ensurer в `newOpencodePort` внёс `e01b3053` от 2026-11-20 05:52. Вызов `newOpencodePort` из wake-адаптера внёс `83447689` от 2026-11-20 16:33, PR 392. Противоречие между комментарием в `cmd/beacon/mcp.go:1513-1515` и кодом существует с PR 392.

## Проверка

Использовались только чтение файлов, rg по явным каталогам и read-only команды git log, git blame и git show. Команды `go test`, `go build`, `beacon gates` не запускались. Инструменты `tools.beacon` не вызывались. Исправление не применялось.

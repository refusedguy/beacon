# Evals для beacon MCP

Набор локальных проверок в стиле Harbor. Раннер печатает таблицу с ожидаемым
результатом, фактическим результатом и вердиктом `PASS`/`FAIL`. В конце выводится
строка вида `9 passed, 0 failed`. При наличии `FAIL` код выхода равен 1.

## Запуск

```bash
GO=/tmp/opencode/go126/bin/go bash evals/run.sh
```

Зависимости берутся из module cache, `GOPROXY=off`, GitHub-токены удаляются из
окружения дочернего процесса. Сеть и токен не нужны.

Eval-код лежит в отдельном модуле `github.com/refusedguy/beacon/evals`
(`evals/go.mod` с `replace` на родительский модуль). Корневой `go test ./...`
в него не заходит, а гейт слоёв не считает evals производственным пакетом.
Файлы имеют build tag `eval`, поэтому сборка обычных пакетов их тоже не видит.
`evals/run.sh` остаётся единственной командой запуска.

## Как устроен прогон

Раннер использует официальный `github.com/modelcontextprotocol/go-sdk`, а именно
`CommandTransport`: поднимает MCP-сервер отдельным процессом и разговаривает с
ним по stdin/stdout. Свой JSON-клиент не написан.

Штатная команда `beacon mcp` вызывает `mcp.Run`, которая собирает сервер через
`NewServer()` без зависимостей. Там нет `Store`, поэтому обработчики
`watch_pr`, `get_result`, `ack` и `cancel_watch` отвечают `PERSIST_FAILED: ...
store is nil`. Передать Store через env или флаг нельзя. Поэтому раннер собирает
eval-специфичную точку входа `evals/cmd/evalserver`: она вызывает
`mcp.NewServerWithDeps` с реальными зависимостями:

- `Store`: SQLite через `internal/infra/store` во временном `XDG_STATE_HOME`;
- `Stamper`: `mcp.NewSQLObligationStamper`;
- `Writer`: `mcp.NewSQLWaitResultWriter`;
- `Events`: `mcp.NewSQLCancelEventLog`;
- `Snapshot`: `mcp.SnapshotFunc` поверх `db.ActiveWatches()`.

Обработчики не подменяются заглушками: проверяется реальный код из
`internal/infra/mcp`. `CIProvider` и `VerdictSource` не нужны этим девяти
кейсам: `headSHA` передан явно, а `wait_result` в прогон не входит. Если в
будущем понадобится eval для `wait_result`, ему потребуется реальный источник
вердикта, и его нельзя будет заменить фейком молча.

Для каждого прогона создаётся временный каталог с отдельными `XDG_STATE_HOME`,
`XDG_DATA_HOME`, `XDG_CACHE_HOME` и `XDG_CONFIG_HOME`. После прогона каталог
удаляется. Пользовательское состояние не трогается.

## Что проверяет каждый eval

| Имя | Что делает | Ожидаемый результат |
| --- | --- | --- |
| `01_handshake` | Читает результат `initialize` | `serverInfo.name=beacon`, версия непустая |
| `02_catalog` | Запрашивает `tools/list` | Ровно семь контрактных имён, без лишних |
| `03_stats` | Вызывает `beacon_stats` | `isError=false`, JSON с полями `activeWatches`, `budget`, `ladder`, `persist`, `version` |
| `04_watch` | Вызывает `beacon_watch_pr` с явным `headSHA` | `isError=false`, есть `watchID`, `revision`, `status`, `verdict` |
| `05_list` | После watch вызывает `beacon_list_running` | `watchID` из `04_watch` присутствует в `watches` |
| `06_get` | Вызывает `beacon_get_result` по `watchID` | `isError=false`, есть `status` и `verdict` |
| `07_ack` | Вызывает `beacon_ack` с `watchID` и `obligationID` | `ok=true` |
| `08_cancel` | Вызывает `beacon_cancel_watch` с `watchID` | `ok=true` |
| `09_ship_verdict` | Собирает `domain.Snapshot` с зелёными обязательными проверками и `ShipFacts{Build, Audit, TDD}` и вызывает `domain.Evaluate` | с `ShipFacts` вердикт `ready`; без `ShipFacts` вердикт не `ready` |

`09_ship_verdict` работает в процессе раннера, без MCP-сервера. Он импортирует
чистую функцию `domain.Evaluate` из родительского модуля. Позитивный контроль
показывает, что `ready` достижим: зелёный rollup, `MERGEABLE`, `CLEAN`, валидная
композиция suite и заполненные факты дают `VerdictReady`. Негативный контроль
берёт тот же Snapshot с нулевыми `ShipFacts` и ожидает вердикт не `ready`.
Если убрать `ShipAllowed` из `Evaluate`, негативный контроль начнёт возвращать
`ready` и eval упадёт. Так eval ловит регрессию T3.72, которую не ловили
восемь кейсов: `ready` недостижим, когда `Ship` не заполняется.

`headSHA` задан константой `0123...4567`. Все вызовы несут `_meta` с ключом
`ai.opencode/sessionID`: обработчики `get_result` и `cancel_watch` читают оттуда
идентификатор вызывающего, а `watch_pr` использует то же значение как
`notify.target`. Так обеспечивается связь watch → obligation → ack.

## Почему правилась схема

`05_list` падал на валидации выходной схемы go-sdk. Причина: у нового Watch
`domain.Watch.Flaky.Used` и `domain.Watch.Checks`: nil-карты, которые
marshalятся в `null`, а выведенная схема требовала `object`. В
`internal/infra/mcp/schema.go` схема `beacon_list_running` теперь допускает
`null` или `object` только для этих двух полей. Типы значений внутри карт и
остальные поля не ослаблены. eval-сервер явно передаёт эту схему в
`Tool.OutputSchema`, потому что `NewServerWithDeps` регистрирует инструмент без
неё и go-sdk выводит схему по Go-типу. Логика `tool_list_running.go` не менялась.

## Разрешения гейта возможностей

Capabilities-гейт видит `os/exec` и файловые вызовы в `evals/` как зону
`outside`. В `tools/beacon-gates/gate/config/permanent.json` добавлены три
строки только для `evals/main.go` и `evals/cmd/evalserver/main.go`: запуск
subprocess через `CommandTransport`, временный XDG-каталог и SQLite. Для
производственных путей исключений не добавлено. Конфиг вшит в бинарник гейтов
через `go:embed`, поэтому бинарник `tools/beacon-gates/beacon-gates`
пересобран теми же флагами, что использует workflow `gates-binary.yml`.

## Ограничения честно

Эти evals проверяют реальные обработчики, реальный SQLite-store и реальный
протокол MCP. Они не проверяют штатную точку входа `beacon mcp` как она есть:
в ней `Store` не подключён. Инструмент `beacon_wait_result` в набор не входит:
ему нужен `VerdictSource`, и подменять его фейком в eval не стали.

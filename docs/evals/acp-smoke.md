# ACP smoke: opencode acp

## Что проверяется

Тест `internal/infra/wake/acp_smoke_test.go` спавнит процесс `opencode acp` и общается с ним по JSON-RPC 2.0 через stdin/stdout. Проверяются три вызова протокола: `initialize`, `session/new` и `session/load`. Код Beacon в прогоне не участвует: это проверка внешнего бинарника, а не адаптера.

Транспорт взят из спецификации ACP v1: одно JSON-RPC-сообщение на строку, без заголовков `Content-Length`. Официального Go SDK у ACP нет, поэтому в тесте используется plain JSON-RPC. Это не самописный клиент для проверок Beacon: для Beacon MCP в `evals/main.go` применяется `mcpsdk.CommandTransport`.

## Как запустить

```
go test ./internal/infra/wake -run TestOpenCodeACPHandshakeAndSession -v -count=1
```

Бинарник ищется в `PATH`, путь можно задать переменной `OPENCODE_BIN`. Если `opencode` не найден, тест пропускается с пояснением, поэтому `go test ./...` в CI не падает из-за отсутствующего бинарника. Тест закрывает stdin и завершает процесс через `t.Cleanup`.

## Наблюдаемый результат

Прогон от 20 ноября 2026 года:

- `opencode --version`: `v2.3.0`.
- `initialize` с `protocolVersion: 1` вернул `protocolVersion: 1`, `agentInfo.name=OpenCode`, `agentInfo.version=2.3.0` и `agentCapabilities.loadSession=true`. Сервер также объявил `mcpCapabilities.http=true`, `promptCapabilities.image=true` и `sessionCapabilities` с `resume`, `close`, `delete`, `list`, `fork`, `additionalDirectories`.
- `session/new` с абсолютным `cwd` и пустым `mcpServers` вернул непустой `sessionId` вида `ses_...`.
- `session/load` с этим `sessionId` вернул результат без RPC-ошибки.
- В ручном пробном прогоне stderr был пустым, а после закрытия stdin процесс вышел с кодом 0.
- Время теста около 6,8 секунды.

Вызовы `session/prompt` и доставка wake-указателя здесь не проверяются. Для этого нужны отдельные тесты адаптера, например карточка T3.180.

## Почему это доказательство

Тест читает ответы реального процесса, а не подставного сервера. Утверждения привязаны к наблюдаемым полям ответа: согласие по версии протокола, непустой `agentInfo`, непустой `sessionId` и успешный `session/load`. Если `opencode acp` перестанет поднимать сервер или принимать любой из трёх вызовов, тест упадёт с текстом ошибки и содержимым stderr.

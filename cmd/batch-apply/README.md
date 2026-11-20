# batch-apply и самоиспользование цикла

`batch-apply` превращает структурированные JSON-отчёты в ADR, карточки и
eval-специи. Оркестратор пользуется этой тулзой сам, а не пишет карточки
вручную (ADR 0071). Watch регистрируется через Beacon MCP, вердикт пишет
CI-мост в store, а пробуждение сессии идёт настоящим watchID.

## Команды

```sh
go run ./cmd/batch-apply -dir <batch-dir> -check
go run ./cmd/batch-apply -dir <batch-dir> -dry-run
go run ./cmd/batch-apply -dir <batch-dir> -commit
```

`-check` только валидирует JSON и печатает компактную сводку. `-dry-run`
показывает файлы без записи. `-commit` пишет документы и коммит их от имени
текущего пользователя git. Невалидные JSON пропускаются, существующие
карточки при создании не перезаписываются; перезапись разрешена только для
закрытий из `card_closures`.

## Замкнутый цикл

1. Исследователь кладёт JSON в каталог пакета.
2. Оркестратор запускает `-check`, затем `-dry-run` и `-commit`.
3. Beacon MCP регистрирует Watch на head PR с адаптером `opencode` и target
   из `_meta` или `notify.target`.
4. CI-мост пишет `verdict=ready` или `retry` в store и будит подписчика.
5. `beacon_get_result` и `beacon_ack` читают вердикт, `readAt` появляется
   в store. Оркестратор доверяет только этому чтению, а не idle-отчёту.

Ключ `_meta` канонический: `beacon/sessionID`. Старый
`ai.opencode/sessionID` остаётся совместимым. Если ни ключа, ни
`notify.target` нет, адаптер возвращает skip: промпт не шлётся и квитанция
не пишется.

## Проверка

`go test ./...` в репозитории зелёный. Внешние факты шага 5 проверяются
тестом `TestChainWatchCIPointerWakeRead` в `internal/infra/cibridge`: он
читает вердикт и `readAt` из настоящего store, а не из внутренних массивов
фейка.

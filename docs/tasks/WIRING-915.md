# WIRING-915. Подключить локальный TUI board к CLI
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: cmd/beacon/main.go, tools/beacon-gates/gate/config/layers.json, cmd/beacon/board.go, internal/tui/board.go
OwnerFiles: cmd/beacon/main.go, tools/beacon-gates/gate/config/layers.json, cmd/beacon/board.go, internal/tui/board.go
Зависимости: PR 915
Ссылка: PR 915
SourceWatch: watch:refusedguy/beacon:915:87af1480fdcb5664aad8156c01b060c3a0857961
PR: 915
Агент: нет
Время: нет
Цель: Подключить локальный TUI board к CLI

Контекст: PR 915 добавил модель Bubble Tea, рендер Lip Gloss, граф зависимостей и tabwriter fallback. Команда ещё не зарегистрирована в dispatch, поэтому `beacon board` не запускается.

## Причина

Матрица `tools/beacon-gates/gate/config/layers.json` разрешает `cmd` только к `internal/infra`, `internal/app` и `internal/domain`. Ребро `cmd` к `internal/tui` запрещено, а оба файла правки (`cmd/beacon/main.go` и сама матрица) лежат вне OwnerFiles IDX-10. Поэтому dispatch-ветка и адаптер к `tui.Run` вынесены в отдельную карточку.

## Шаги

1. Дождаться мержа PR 915.
2. Добавить разрешённое ребро `cmd` к `internal/tui` в `tools/beacon-gates/gate/config/layers.json`. Гейты `layers-ast` и `layers-list` обязаны дать одинаковый результат.
3. В `cmd/beacon/main.go` добавить `case "board": return boardRunner(c, args[1:])`, а также строку команды в `usageText`.
4. В `cmd/beacon/board.go` заменить стандартный раннер адаптером: прочитать карточки через `taskstore.LoadCards`, преобразовать их в `[]tui.Card`, при интерактивном терминале вызвать `tui.Run`, иначе оставить `RenderFallback`.
5. Проверить, что `beacon board` работает без сети и показывает те же карточки, что индекс.

## Проверка

- Список OwnerFiles совпадает с полем «Владелец файлов».
- Карточка лежит в PR 915.
- `beacon board --format table` печатает те же строки, что индекс карточек.
- `beacon board` в терминале открывает модель Bubble Tea без обращений к сети.

## Приёмка

Команда зарегистрирована, CI зелёный, запись `beacon deferred` для этой карты переведена в `done`.

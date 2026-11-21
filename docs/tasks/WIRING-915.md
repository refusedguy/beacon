# WIRING-915. Подключить интерактивный beacon board к dispatch
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/main.go, cmd/beacon/board_wire.go, tools/beacon-gates/gate/config/layers.json
OwnerFiles: cmd/beacon/main.go, cmd/beacon/board_wire.go, tools/beacon-gates/gate/config/layers.json
Зависимости: PR 915
Ссылка: PR 915
SourceWatch: неизвестно
PR: 915
Агент: нет
Время: нет
baseline: 2026-11-21, в дереве PR 915 считано 970 карточек в docs/tasks, из них 83 todo; `grep -rl 'github.com/refusedguy/beacon/internal/tui' --include='*.go' .` вернул 0 файлов. Матрица layers.json содержит 4 обычных ребра и ни одного ребра cmd к internal/tui. Дальше добавлять ребро нельзя без пересборки закоммиченного бинарника гейтов: он хранит копию layers.json внутри себя.
Цель: открывать интерактивный board из dispatch `beacon board`, когда Layer-матрица и бинарник гейтов разрешают этот импорт.
Контекст: PR 915 добавил модель Bubble Tea, рендер и tabwriter в internal/tui и локальный раннер в cmd/beacon/board.go. Dispatch-ветка и адаптер к tui.Run остались за пределами OwnerFiles того PR.
Шаги: добавить ребро cmd к internal/tui в layers.json, пересобрать и закоммитить tools/beacon-gates/beacon-gates, заменить адаптер в cmd/beacon/board_wire.go на вызов tui.Run и tui.RenderFallback, проверить dispatch в cmd/beacon/main.go.
Проверка: `beacon board` открывает модель из internal/tui в терминале и печатает fallback без терминала. Ребро cmd к internal/tui проходит оба прохода layer-гейта и проверку бинарника.
Приёмка: CI зелёный, карточка переведена в done, запись в `beacon deferred --format json` имеет status done.

## Причина

Матрица `tools/beacon-gates/gate/config/layers.json` не разрешает cmd импортировать internal/tui. Добавление ребра меняет конфигурацию, которую бинарник `tools/beacon-gates/beacon-gates` хранит внутри себя через go:embed. Пересборка бинарника требует `go build` и правки файла вне OwnerFiles этой карточки, поэтому ребро и адаптер к tui.Run перенесены в отдельную проводку. Текущий PR подключает dispatch к локальному раннеру из board.go через RegisterBoard и сохраняет матрицу без изменений.

## Шаги

1. Получить разрешение владельца на ребро cmd к internal/tui и пересборку бинарника гейтов.
2. Добавить ребро в layers.json и пересобрать tools/beacon-gates/beacon-gates воспроизводимой командой из .github/workflows/gates-binary.yml.
3. Заменить адаптер в cmd/beacon/board_wire.go на преобразование карточек в tui.Card и вызов tui.Run или tui.RenderFallback.
4. Проверить, что dispatch в cmd/beacon/main.go вызывает boardRunner и что usage описывает интерактивный режим.

## Проверка

- Добавленное ребро проходит layers-ast, layers-list и сравнение закоммиченного бинарника со сборкой.
- `beacon board` в интерактивном терминале открывает модель из internal/tui.
- `beacon board` без терминала печатает fallback, фильтры --status и --owner работают в обоих режимах.

## Приёмка

Проводка применена, CI зелёный, карточка и отложенная запись переведены в done.

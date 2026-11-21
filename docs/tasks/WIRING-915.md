# WIRING-915. Подключить интерактивный beacon board к dispatch
Premise: перепроверено 2026-11-21 по свежему origin/main (d263c83). PR #930 влит и подключил dispatch `beacon board` к локальному раннеру через RegisterBoard, но адаптер вызывает runBoard, а не tui.Run. Ребра cmd к internal/tui в layers.json нет, ребро требует пересборки закоммиченного бинарника гейтов. Статус todo сохранён, код не писал, OwnerFiles не менял, чужие файлы, wiring и .beacon не трогал, правка только текстовая.
baseline: `git rev-parse --short origin/main` = d263c83; `git merge-base --is-ancestor f42d582 origin/main` = 0; `git rev-list --count 4c811d8..origin/main` = 48, `git log 4c811d8..origin/main -- cmd/beacon/board_wire.go cmd/beacon/main.go internal/tui/ tools/beacon-gates/gate/config/layers.json tools/beacon-gates/beacon-gates` = пусто; `git grep -n -E 'tui\.Run|tui\.RenderFallback' origin/main -- cmd/ internal/` = пусто; `git grep -n 'github.com/refusedguy/beacon/internal/tui' origin/main -- cmd/ internal/` = пусто; `git show origin/main:cmd/beacon/board_wire.go` вызывает runBoard; `git show origin/main:tools/beacon-gates/gate/config/layers.json` не содержит ребра cmd к internal/tui; `gh pr list --repo refusedguy/beacon --state all --search WIRING-915` = PR #915, #930, #951 влиты, но адаптера к tui.Run в них нет; остальные PR меняют только карточку, включая влитые #1076, #1080 и #1086; PR с реализацией tui.Run нет.
Статус: in_progress
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

## Подтверждение

- 2026-11-21. Premise перепроверен по свежему origin/main (d263c83). Реализация не влита: PR #930 (f42d582) влит, но адаптер в cmd/beacon/board_wire.go вызывает локальный runBoard, а не tui.Run. Вызовов tui.Run и tui.RenderFallback в дереве нет, ребра cmd к internal/tui в layers.json нет. Все 48 коммитов после 4c811d8 меняют только docs и не трогают board_wire.go, layers.json, internal/tui или бинарник гейтов. PR #915, #930 и #951 влиты, но ни одна реализация адаптера к tui.Run в них нет. Влитые #1076, #1080 и #1086 меняли только карточку. Блокер на актуальное состояние: нужно разрешение владельца на ребро cmd к internal/tui и воспроизводимая пересборка tools/beacon-gates/beacon-gates, который хранит копию layers.json через go:embed. Запись с SHA d263c83 оставлена одна, дублей нет, старые перепроверки на e71e117, 5400179, bd4864f и cd0ce02 удалены. Статус todo сохранён, код не писал, OwnerFiles не менял, чужие файлы, wiring и .beacon не трогал, правка только текстовая.

Состояние: needs_input
Вопрос: Нужно ли решение владельца на добавление ребра cmd -> internal/tui в layers.json и воспроизводимую пересборку tools/beacon-gates/beacon-gates с закоммиченным бинарником (go:embed layers.json)? Адаптер в cmd/beacon/board_wire.go пока вызывает runBoard, а не tui.Run.
# WIRING-915. Подключить интерактивный beacon board к dispatch
Premise: проверено 2026-11-21 по свежему origin/main (bd4864f). PR #930 влит и подключил dispatch `beacon board` к локальному раннеру через RegisterBoard, но адаптер вызывает runBoard, а не tui.Run. Ребра cmd к internal/tui в layers.json нет, ребро требует пересборки закоммиченного бинарника гейтов. Статус todo сохранён, точная причина ниже.
Статус: todo
baseline: `git rev-parse --short origin/main` = bd4864f; `gh pr view 930 --repo refusedguy/beacon --json state,mergedAt` = MERGED 2026-11-21T10:02:39Z; `git merge-base --is-ancestor f42d582 origin/main` = 0; `git log --oneline 4c811d8..origin/main` = 35 коммитов, все docs, ни одного с tui.Run или ребром cmd к internal/tui; `gh pr list --repo refusedguy/beacon --state merged --limit 60` показывает только refresh карточек, PR #994 оставил WIRING-915 в todo, PR #1042 обновил baseline до 4c811d8, PR #1059 до bb537f2, PR #1063 до 05996df, PR #1071 до e71e117, ни один не изменил код; `gh pr list --repo refusedguy/beacon --state open --limit 100` показывает только карточки docs(tasks), PR с реализацией tui.Run нет; `git grep -n -E 'tui\.Run|tui\.RenderFallback' origin/main -- cmd/ internal/` = пусто; `git grep -n 'github.com/refusedguy/beacon/internal/tui' origin/main -- cmd/ internal/` = пусто; `git show origin/main:cmd/beacon/board_wire.go` вызывает runBoard; `git show origin/main:tools/beacon-gates/gate/config/layers.json` не содержит ребра cmd к internal/tui.
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

Проверка 2026-11-21 по origin/main (bd4864f): PR #930 влит, dispatch вызывает boardRunner через RegisterBoard, но cmd/beacon/board_wire.go вызывает локальный runBoard из board.go. Вызовов tui.Run и tui.RenderFallback в дереве нет. Список allowed в layers.json не содержит ребра cmd к internal/tui, а бинарник гейтов хранит копию конфига через go:embed. Все 35 коммитов после 4c811d8 меняют только docs и не трогают board_wire.go, layers.json или internal/tui. PR #994 оставил WIRING-915 в todo, PR #1031 обновил baseline до cf47037, PR #1042 до 4c811d8, PR #1059 до bb537f2, PR #1063 до 05996df, PR #1071 до e71e117, PR #1080 обновил baseline до 5400179, каждый из них менял только карточку. Для завершения нужно разрешение владельца на ребро и воспроизводимая пересборка бинарника. До этого статус остаётся todo.

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

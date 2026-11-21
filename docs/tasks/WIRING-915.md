# WIRING-915. Подключить интерактивный beacon board к dispatch
Premise: проверено 2026-11-21 по свежему origin/main (4c811d8). PR #930 влит и подключил dispatch `beacon board` к локальному раннеру через RegisterBoard, но адаптер вызывает runBoard, а не tui.Run. Ребра cmd к internal/tui в layers.json нет, ребро требует пересборки закоммиченного бинарника гейтов. Статус todo сохранён, точная причина ниже.
Статус: todo
baseline: `git rev-parse --short origin/main` = 4c811d8; `gh pr view 930 --repo refusedguy/beacon --json state,mergedAt` = MERGED 2026-11-21T10:02:39Z; `git merge-base --is-ancestor f42d582 origin/main` = 0; `git log --oneline cf47037..origin/main` = 10 коммитов на момент проверки, все docs(tasks), ни одного с tui.Run или ребром cmd к internal/tui; `gh pr list --repo refusedguy/beacon --state merged --limit 40` показывает только refresh карточек, PR #994 явно оставил WIRING-915 в todo; `gh pr list --repo refusedguy/beacon --state open --limit 100` показывает только карточки docs(tasks), на момент проверки #1034; `git grep -n -E 'tui\.Run|tui\.RenderFallback' origin/main -- cmd/ internal/` = пусто; `git grep -n '"to": "internal/tui"' origin/main -- tools/beacon-gates/gate/config/layers.json` = пусто; `git show origin/main:cmd/beacon/board_wire.go` вызывает runBoard; `git show origin/main:tools/beacon-gates/gate/config/layers.json` не содержит ребра cmd к internal/tui.
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

Проверка 2026-11-21 по origin/main (4c811d8): PR #930 влит, dispatch вызывает boardRunner через RegisterBoard, но cmd/beacon/board_wire.go вызывает локальный runBoard из board.go. Вызовов tui.Run и tui.RenderFallback в дереве нет. Список allowed в layers.json не содержит ребра cmd к internal/tui, а бинарник гейтов хранит копию конфига через go:embed. Все коммиты после cf47037 меняют только карточки docs(tasks) и не трогают board_wire.go, layers.json или internal/tui. PR #994 явно оставил WIRING-915 в todo, PR #1031 обновил baseline карточки до cf47037 и тоже не изменил код. Для завершения нужно разрешение владельца на ребро и воспроизводимая пересборка бинарника. До этого статус остаётся todo.

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

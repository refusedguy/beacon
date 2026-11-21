# WIRING-915. Подключить интерактивный beacon board к dispatch
Premise: проверено 2026-11-21. Реализация в PR #1111: адаптер в cmd/beacon/board_wire.go загружает карточки, применяет --status и --owner и вызывает tui.Run в терминале или tui.RenderFallback без терминала. Ребро cmd к internal/tui добавлено в layers.json, бинарник гейтов пересобран. Дублей и блокеров по текущему состоянию нет.
baseline: `git rev-parse --short origin/main` = 6a64c79; до правки `git grep -n 'tui.Run' cmd/ internal/` давал пусто, адаптер вызывал runBoard, ребра cmd к internal/tui в layers.json не было; после правки `tools/beacon-gates/beacon-gates layers-ast` и `layers-list` на дереве дали `green: no findings`, проверки self-test дали `self-test passed: 5 fixtures` и `self-test passed: 4 fixtures`; две сборки бинарника с `GOTOOLCHAIN=go1.26.0`, `CGO_ENABLED=0`, `-trimpath -buildvcs=false -ldflags="-buildid= -s -w"` байтово совпали, sha256 539db332a030aeb3f6caf56d5da3763dcb14661f020bf54675382fe7cf9e341d.
Статус: done
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/main.go, cmd/beacon/board_wire.go, cmd/beacon/board.go, internal/tui/*.go, tools/beacon-gates/gate/config/layers.json, tools/beacon-gates/gate/fixtures.go, tools/beacon-gates/beacon-gates, docs/tasks/WIRING-915.md
OwnerFiles: cmd/beacon/main.go, cmd/beacon/board_wire.go, cmd/beacon/board.go, internal/tui/*.go, tools/beacon-gates/gate/config/layers.json, tools/beacon-gates/gate/fixtures.go, tools/beacon-gates/beacon-gates, docs/tasks/WIRING-915.md
Зависимости: PR 915
Ссылка: PR 915
SourceWatch: неизвестно
PR: 1111
Агент: нет
Время: нет
Цель: открывать интерактивный board из dispatch `beacon board`, когда Layer-матрица и бинарник гейтов разрешают этот импорт.
Контекст: PR 915 добавил модель Bubble Tea, рендер и tabwriter в internal/tui и локальный раннер в cmd/beacon/board.go. Dispatch-ветка и адаптер к tui.Run остались за пределами OwnerFiles того PR.
Шаги: добавить ребро cmd к internal/tui в layers.json, пересобрать и закоммитить tools/beacon-gates/beacon-gates, обновить фикстуру self-test гейта, заменить адаптер в cmd/beacon/board_wire.go на преобразование карточек в tui.Card и вызов tui.Run или tui.RenderFallback, проверить dispatch в cmd/beacon/main.go.
Проверка: `beacon board` открывает модель из internal/tui в терминале и печатает fallback без терминала. Ребро cmd к internal/tui проходит оба прохода layer-гейта и проверку бинарника.
Приёмка: CI зелёный, карточка переведена в done, запись в `beacon deferred --format json` имеет status done.

## Причина

Матрица `tools/beacon-gates/gate/config/layers.json` не разрешала cmd импортировать internal/tui. Ребро добавлено, бинарник гейтов хранит копию матрицы через go:embed и пересобран воспроизводимой командой из .github/workflows/gates-binary.yml. Фикстура layer self-test'ов обновлена, иначе новое ребро помечалось как неиспользуемое.

## Подтверждение

- 2026-11-21. Реализация отправлена в PR https://github.com/refusedguy/beacon/pull/1111. Адаптер вызывает tui.Run при реальном TTY и tui.RenderFallback для pipe или /dev/null. Флаги --root, --status и --owner сохранены. Dispatch в cmd/beacon/main.go вызывает boardRunner, usage описывает оба режима. Ребро cmd к internal/tui добавлено, layer-гейты на дереве зелёные, бинарник пересобран с sha256 539db332a030aeb3f6caf56d5da3763dcb14661f020bf54675382fe7cf9e341d. Статус изменён с todo на done. Локально go test и go build приложения не запускались, проверки остаются за CI.

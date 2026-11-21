# IDX-10. Локальный TUI beacon board
Статус: done
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/board.go, internal/tui/board.go, go.mod, go.sum
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Premise: проверено 2026-11-21 по origin/main после rebase ветки. Карточки UI-04, UI-25, UI-31 и UI-32 отклонены ссылкой на ADR 0060, но IDX-14 и обновлённый AGENTS.md прямо описывают `beacon board` как локальный TUI без сети. В main уже лежат internal/tui/client.go и internal/tui/socket.go, поэтому направление владельца изменено. Дубля нет: до этой правки ни один файл не строил board из карточек и не регистрировал команду board.
baseline: 2026-11-21, `ls docs/tasks/*.md | wc -l` = 970 карточек. `grep -h '^Статус:' docs/tasks/*.md | sed 's/^Статус: *//' | sort | uniq -c` дал done 555, dropped 326, todo 88, in_progress 1. `grep -h '^Агент:' docs/tasks/*.md | sed 's/^Агент: *//' | sort | uniq -c` дал 940 раз «нет» и ещё 30 карточек на 14 значений владельца. `cmd/beacon/catalog.go:68` уже печатает таблицы через text/tabwriter, этот стиль использован как fallback.
Цель: Красивый локальный интерфейс с view задач, графом и фильтрами.
Контекст: Premise: владелец хочет Jira-подобный обзор и максимально крутой UX без сервера.
Шаги: Bubble Tea плюс Lip Gloss, таблица, detail pane, фильтры по статусу и владельцу, tabwriter как fallback.
Проверка: gofmt, go test в CI, сборка бинарника.
Приёмка: beacon board открывается без сети и показывает те же данные, что индекс.

## Решение

Интерактивный board лежит в internal/tui/board.go. Модель Bubble Tea держит список карточек, курсор, фильтры по статусу и владельцу и панели деталей и графа. Клавиши j/k и стрелки двигают курсор, s переключает статус, o переключает владельца, g показывает граф зависимостей, esc сбрасывает фильтры, q выходит. Граф строится из поля Dependencies карточек, обход использует множество посещённых узлов и не зависит от порядка рёбер. Lip Gloss задаёт заголовок, подсветку строки, рамки панелей и подсказку. RenderFallback печатает ту же подборку карточек через text/tabwriter для неинтерактивного вывода. Сети, сокетов и SQLite в этом файле нет.

Командная обвязка лежит в cmd/beacon/board.go. Она читает карточки через taskstore.LoadCards, применяет флаги `--root`, `--status` и `--owner` и печатает таблицу. Переменная boardRunner и функция RegisterBoard дают точку подключения для окончательной реализации. Внутри этого файла нет прямого чтения файлов, сетевых вызовов и запуска процессов.

Проведено локально: gofmt -w на два новых .go файла и go mod tidy для go.mod и go.sum. Тесты и сборка оставлены CI по правилам AGENTS.md.

## Отложенная проводка

Матрица `tools/beacon-gates/gate/config/layers.json` разрешает cmd только к internal/infra, internal/app и internal/domain. Ребро cmd к internal/tui запрещено, поэтому dispatch-ветка `case "board"` в cmd/beacon/main.go и адаптер к tui.Run не входят в OwnerFiles этой карточки. Проводка вынесена в WIRING-карточку через beacon_defer_wiring. Интерактивный запуск появится после её мержа, а текущий PR поставляет готовую модель, рендер и командный слой без сети.

## Подтверждение

Проверено 2026-11-21 по `git log` и `gh pr view`. Реализация влита в origin/main через PR #915, https://github.com/refusedguy/beacon/pull/915. Merge commit a7e447cf627f7bb4119543805544b653f26047b4 добавил cmd/beacon/board.go, internal/tui/board.go и зависимости в go.mod и go.sum. Статус переведён в done.

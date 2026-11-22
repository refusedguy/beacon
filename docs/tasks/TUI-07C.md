# TUI-07C. Тест текстового fallback без TTY
Статус: dropped
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/board_wire.go
Зависимости: TUI-01, TUI-02, TUI-03
Ссылка: ADR 0306
Агент: нет
Время: нет
Цель: Не обещать тест stdout-fallback, пока детект TTY вывода не имеет владельца.
Контекст: Отчёт исполнителя исходной TUI-07: cmd/beacon/board_wire.go:95 boardHasTerminal проверяет только stdin. cmd/beacon/watch.go:212 runWatch и internal/tui/app.go:125 Run пишут в io.Writer без проверки stdout. TUI-01, TUI-02 и TUI-03 меняют main.go/board.go, app.go/board.go и client.go/watch.go. Никто из них не владеет board_wire.go, поэтому детект stdout TTY остаётся без исполнителя даже после их мержа.
Шаги: Дизайн брошен. Тест требует production-правки в board_wire.go, watch.go и app.go, которые находятся вне OwnerFiles этой карточки. Вернуть карточку после того, как владелец назначит правку board_wire.go в одной из TUI-01, TUI-02 или TUI-03.
Проверка: Проверка не требуется.
Приёмка: Активной карточки TTY-теста в пакете нет. Владельцы production-правки указаны в контексте.

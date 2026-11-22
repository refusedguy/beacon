# FTE-06. Добавить beacon tui и beacon up --tui
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/main.go, cmd/beacon/catalog.go, cmd/beacon/up.go, cmd/beacon/board_wire.go
Зависимости:
Ссылка: docs/evals/tui-live-single-screen.md:11-16,37, docs/research/research-feature-gaps.md:16,37, docs/research/research-dir3-tui.md:34,44
Агент: нет
Время: нет
baseline: cmd/beacon/main.go:253-286 не содержит dispatch для tui. cmd/beacon/up.go:171-176 объявляет --interval, --once и --channels, флага --tui нет. board --live уже работает через cmd/beacon/board.go:42 и :114, поэтому обещание eval устарело в части третьей точки входа.
Цель: beacon tui и beacon up --tui ведут в тот же живой раннер, что и beacon board --live.
Контекст: docs/evals/tui-live-single-screen.md:11-16 фиксирует три точки входа как обещание TU3-01. eval требует, что все три доходят до App.RunLive. Сейчас работает только board --live. TU3-01, TU3-02 и TU3-05 в docs/tasks/ отсутствуют, поэтому обещание осталось без карточки.
Шаги: Зарегистрировать tui в runnerFor и каталоге. Добавить --tui в up. Направить оба пути в тот же адаптер, что и board --live. Сохранить текстовый fallback для pipe.
Проверка: beacon tui --help и beacon what tui возвращают код 0. beacon up --tui --once доходит до рендера живого снимка. Тест вызывает один и тот же App.RunLive из трёх точек входа. Вывод без TTY остаётся текстовым и завершается кодом 0.
Приёмка: Три точки входа работают, eval tui-live-single-screen проходит, CI зелёный.

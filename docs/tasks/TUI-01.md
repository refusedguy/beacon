# TUI-01. Подключить живой экран через порт в cmd/beacon
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/main.go, cmd/beacon/board.go
Зависимости: 
Ссылка: ADR 0306
Агент: нет
Время: нет
Цель: Открыть живой экран командой beacon board --live без прямого импорта internal/tui в слой cmd и без нового сетевого кода в этом слое.
Контекст: Источник: docs/research/audit-03-tui.md, таблица строки 1 и раздел про легальную обвязку. cmd/beacon/main.go:245 dispatch держит board на boardRunner. cmd/beacon/board_wire.go:6 installBoardAdapter вызывает локальный runBoard. internal/tui/board.go:357 Run создаёт Bubble Tea программу, но в dispatch не доходит. Матрица layers.json:15 запрещает cmd импортировать internal/tui. PR #1111 head 08147da уже меняет адаптер статического board и матрицу.
Шаги: Проверить состояние PR #1111 и не дублировать его адаптер. Создать cmd/beacon/tui.go с портом из трёх методов: чтение снимка, подписка на события, отправка решения. Построить живой раннер поверх существующего httpapi.ProjectionClient. В cmd/beacon/board.go добавить флаг --live и выбор раннера через RegisterBoard. Не импортировать net/http, os/exec, os.ReadFile и os.WriteFile в cmd. Добавить новый файл в OwnerFiles карточки при получении задачи. Согласовать ребро матрицы с TUI-06.
Проверка: CI зелёный. Проверка cmd на запрещённые импорты проходит. beacon board --live открывает живой экран, beacon board без флага печатает прежнюю таблицу. Повторного PR на статический адаптер нет.
Приёмка: Один dispatch ведёт в статический и живой режимы. Клиент живого режима живёт за портом, а не в cmd.

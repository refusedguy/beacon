# IDX-10. Локальный TUI beacon board
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/board.go, internal/tui/board.go, go.mod, go.sum
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Красивый локальный интерфейс с view задач, графом и фильтрами.
Контекст: Premise: владелец хочет Jira-подобный обзор и максимально крутой UX без сервера.
Шаги: Bubble Tea плюс Lip Gloss, таблица, detail pane, фильтры по статусу и владельцу, tabwriter как fallback.
Проверка: gofmt, go test в CI, сборка бинарника.
Приёмка: beacon board открывается без сети и показывает те же данные, что индекс.

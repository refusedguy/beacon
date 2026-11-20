# BS.39. Control socket для агентов
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/tui/socket.go
Зависимости: BS.38
Ссылка: ADR 0194
Агент: нет
Время: нет
Цель: Агент может управлять запущенным TUI.
Контекст: openrig и Crush имеют control socket для запущенного TUI.
Шаги: Локальный socket, команды ack/cancel/claim/release, идемпотентность по watchID и revision.
Проверка: Команда через socket не пишет в БД напрямую.
Приёмка: Socket доступен только локальному пользователю.

## Подтверждение
- Premise проверен 2026-11-20, дубль не создан. Функциональность уже в main: PR #493 (commit c568028) добавил `internal/tui/socket.go` и `internal/tui/socket_test.go`. Сервер поднимает локальный Unix socket с режимом 0600, принимает команды ack, cancel, claim и release, возвращает закэшированный ответ по ключу command+watchID+revision. Команды идут через интерфейс `Handler`, файл не открывает store и не пишет в SQLite. Проверка и приёмка карточки закрыты, код в OwnerFiles писать не нужно. Подключение слушателя к `app.go` и `main.go` отложено отдельной карточкой WIRING-493, её файлы принадлежат другим задачам.

# BS.39. Control socket для агентов
Статус: todo
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

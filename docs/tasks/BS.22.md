# BS.22. Audit log актора и ingress
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/store/audit.go
Зависимости: BS.12
Ссылка: ADR 0188
Агент: нет
Время: нет
Цель: Каждая привилегированная команда имеет автора.
Контекст: MCP, TUI и webhook имеют разные ingress.
Шаги: Таблица audit(actor, ingress, command_id, action), запись в транзакции.
Проверка: Мерж и wake имеют запись с актором.
Приёмка: Аудит доступен через /api/v1/audit.

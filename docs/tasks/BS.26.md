# BS.26. Журнал доставок с UNIQUE(delivery_id)
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/store/webhook_events.go, internal/infra/store/db.go
Зависимости: BS.25
Ссылка: ADR 0193
Агент: нет
Время: нет
Цель: Ручной redelivery не создаёт вторую работу.
Контекст: X-GitHub-Delivery превращается в event_id.
Шаги: UNIQUE(delivery_id), возврат сохранённого результата при повторе.
Проверка: Две доставки с одним id дают одну строку и один refresh.
Приёмка: Статус доставки читаем через API.

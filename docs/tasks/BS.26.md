# BS.26. Журнал доставок с UNIQUE(delivery_id)
Статус: done
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

## Подтверждение
- PREMISE проверен: функционал уже в origin/main, код не дублирован. db.go: createWebhookEventsSQL создаёт таблицу webhook_events с delivery_id TEXT PRIMARY KEY, поэтому один GitHub delivery занимает одну строку. webhook_events.go: RecordDelivery пишет через INSERT OR IGNORE и на повторе возвращает сохранённую строку с флагами Duplicate и Conflict; статус читается через DeliverySeen. webhook_events_test.go: две доставки с одним id дают одну строку и один результат, а повтор с другим телом не меняет статус. Функция добавлена коммитом 27963b5 (T3.375).

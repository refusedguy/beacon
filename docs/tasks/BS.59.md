# BS.59. Роутер авто-уведомлений
Статус: dropped
Фаза: P5
Тип: task
Владелец файлов: internal/app/notify/router.go
Зависимости: BS.29, BS.14
Ссылка: ADR 0198
Агент: нет
Время: нет
Цель: Результат задачи доходит до вызвавшего.
Контекст: Crush доставляет channel ровно в одну сессию.
Шаги: Живой turn, очередь, новый thread; receipt доставки; snooze guard.
Проверка: При двух клиентах событие доставляется один раз.
Приёмка: Headless-сервер продолжает обрабатывать push.

## Подтверждение
- PR #394 DROPPED: Old notify router design (live turn/queue/new thread/snooze) conflicts with the new contract of 7 MCP tools and ses_ targets; live delivery is wake deliverer + obligations; dead T2xx design.

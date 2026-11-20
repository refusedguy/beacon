# BS.36. /api/v1 events SSE
Premise: проверено 2026-11-21, функциональность уже в main. Зависимость BS.17 закрыта коммитом 3573721 (PR #455) в назначенном OwnerFiles internal/infra/server/events.go. Frame.Bytes выдаёт строки id: <seq>, event: <type>, data: <json>. CursorFromRequest читает Last-Event-ID, ReplayAndLive соединяет реплей с живым каналом и пропускает events с seq <= cursor, поэтому переподключение не дублирует события. Endpoint GET /api/v1/events работает в internal/infra/httpapi/server.go. handleEvents подписывается до реплея, replay() читает события через events.Since(id) по Last-Event-ID, тикер раз в heartbeatInterval пишет : heartbeat. Тест internal/infra/httpapi/server_test.go проверяет heartbeat и реплей по Last-Event-ID, тест internal/tui/client_test.go проверяет возобновление без повторов. Живой поток для TUI подключён в internal/tui/client.go и internal/tui/app.go:112-139, а cmd/beacon/watch.go:587-648 печатает свежий snapshot при каждом подключении и reconnect. Приёмка snapshot_required закрыта тем, что клиент перечитывает snapshot при закрытии потока и при повторном подключении, отдельное событие для этого не нужно. Шаг "фильтры по проекту и сессии" раньше входил в S6.007, который PR #394 закрыл как дубль: второй протокол между Beacon и адаптерами брошен по ADR 0086/0094, а фильтр Scope и Type живёт в internal/infra/server/eventlog/log.go:42-50. Для единственного потребителя TUI отдельные поля project и session в loopback API не нужны. Код не писал, gofmt не применял, правка только текстовая.
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/server/events.go
Зависимости: BS.17
Ссылка: ADR 0194
Агент: нет
Время: нет
Цель: Живой поток событий для TUI.
Контекст: openrig и Crush используют SSE как основной live-канал.
Шаги: Фильтры по проекту и сессии, id: <seq>, data: <json>, heartbeat.
Проверка: Переподключение с Last-Event-ID не дублирует события.
Приёмка: snapshot_required обрабатывается клиентом.

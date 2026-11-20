# BS.34. /api/v1 проекции вотчей
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/server/api_watches.go
Зависимости: BS.2, BS.11
Ссылка: ADR 0194
Агент: нет
Время: нет
Цель: TUI читает состояние без доступа к БД.
Контекст: openrig TUI читает /api/ps и /api/rigs/summary.
Шаги: Список вотчей с вердиктом, revision и подписчиком; пагинация.
Проверка: Проекция совпадает с beacon_list_running.
Приёмка: Маршрут только для чтения.

## Подтверждение
- premise проверен 2026-11-20, дубль. Проекция `/api/v1/watches` уже в main. Коммит 8d16352 (PR #471, BS.34) добавил `internal/infra/server/api_watches.go` и тесты к нему. Файл задаёт `WatchReader`, `WatchProjection` и `WatchesPage` с вердиктом, revision и подписчиками, фильтры по verdict и status, пагинацию через `NextCursor` и счётчики, совпадающие с `beacon_list_running`. `Handler` пускает только GET и возвращает 405 на остальные методы, что закрывает приёмку «маршрут только для чтения». Тесты `TestWatchesPageListsVerdictRevisionSubscribers`, `TestWatchesCountsMatchListRunning`, `TestWatchesPagination` и `TestWatchesHandlerReadOnly` покрывают шаги и проверку карточки. Код в OwnerFiles менять не стал, дубль не создан.

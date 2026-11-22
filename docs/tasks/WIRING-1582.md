# WIRING-1582. Показать состояние SharedGate в beacon_stats
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: internal/infra/mcp/tool_stats.go, cmd/beacon/mcp.go
OwnerFiles: см. поле «Владелец файлов»
Зависимости: PR 1582
Ссылка: PR 1582
SourceWatch: watch исходного PR #1582 закрыт после мержа, headSHA не зафиксирован в описании PR
PR: 1582
Агент: нет
Время: нет
Цель: Передать SharedCIWaitSnapshot из store в ответ beacon_stats.
Контекст: PR #1582 добавил SharedGateSnapshot и SharedCIWaitSnapshot в internal/infra/store/observability.go. Снимок не подключён к beacon_stats, поэтому оператор по-прежнему читает состояние gates только по WARNING в логах.
Шаги: Дождаться мержа PR 1582; добавить порт чтения снимка в stats handler в internal/infra/mcp/tool_stats.go; передать реализацию из composition root в cmd/beacon/mcp.go; покрыть чтение снимка тестом; вызвать `beacon deferred done` с watchID из SourceWatch.
Проверка: beacon_stats возвращает CombinedRounds и карту ожидающих по ключам, ActiveWaiters и текст WARNING не изменились, тесты mcp зелёные в CI, запись WIRING-1582 переведена в done.
Приёмка: Проводка применена, запись в `beacon deferred --format json` имеет `status: done`, CI зелёный.

## Причина

OwnerFiles PR #1582 включали только internal/infra/store/observability.go. beacon_stats лежит в internal/infra/mcp, а сборка зависимостей живёт в cmd/beacon. Подключение снимка к статистике вынесли в отдельную карточку.

# WIRING-1582. Показать состояние SharedGate в beacon_stats
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/mcp/tool_stats.go, cmd/beacon/mcp.go
OwnerFiles: см. поле «Владелец файлов»
Зависимости: PR 1582
Ссылка: PR 1582
SourceWatch: watch исходного PR #1582 закрыт после мержа, headSHA не зафиксирован в описании PR
PR: 1582
Агент: ses_wiring1582_executor
Время: 2026-11-22T18:24:54Z
Цель: Передать SharedCIWaitSnapshot из store в ответ beacon_stats.
Контекст: PR #1582 добавил SharedGateSnapshot и SharedCIWaitSnapshot в internal/infra/store/observability.go. Снимок не подключён к beacon_stats, поэтому оператор по-прежнему читает состояние gates только по WARNING в логах.
Шаги: Дождаться мержа PR 1582; добавить порт чтения снимка в stats handler в internal/infra/mcp/tool_stats.go; передать реализацию из composition root в cmd/beacon/mcp.go; покрыть чтение снимка тестом; вызвать `beacon deferred done` с watchID из SourceWatch.
Проверка: beacon_stats возвращает CombinedRounds и карту ожидающих по ключам, ActiveWaiters и текст WARNING не изменились, тесты mcp зелёные в CI, запись WIRING-1582 переведена в done.
Приёмка: Проводка применена, запись в `beacon deferred --format json` имеет `status: done`, CI зелёный.

Причина: Уже влито в main. PR #1906, коммит f48b44b30344f6019cf9847c2e73854acb1c866f (feat(stats): expose shared CI gate snapshot (FTE-03)), добавил в internal/infra/mcp/tool_stats.go порт SharedGateSnapshotReader, метод SetSharedGateSnapshotReader, публикацию CIWaitSnapshot и тест. Проверка git merge-base --is-ancestor f48b44b3 origin/main вернула 0. Привязку адаптера в композитном корне ведёт отдельная карточка WIRING-1906, её OwnerFiles включают server.go и cmd/beacon/mcp.go. Повторного кода и PR с кодом не открываю.
Закрыл: ses_wiring1582_executor
Время закрытия: 2026-11-22T18:26:53Z
## Причина

OwnerFiles PR #1582 включали только internal/infra/store/observability.go. beacon_stats лежит в internal/infra/mcp, а сборка зависимостей живёт в cmd/beacon. Подключение снимка к статистике вынесли в отдельную карточку.

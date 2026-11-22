# WIRING-1906. Подключить SharedGateSnapshotReader в композитном корне MCP
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: internal/infra/mcp/server.go, cmd/beacon/mcp.go
OwnerFiles: см. поле «Владелец файлов»
Зависимости: PR 1906
Ссылка: PR 1906
SourceWatch: watch:refusedguy/beacon:1906:41a480aefcb6a9f05789a1da58faf1b7acc09efc@1795363887:1800
PR: 1906
Агент: нет
Время: нет
Цель: Подключить порт SharedGateSnapshotReader к store.SharedCIWaitSnapshot после мержа PR 1906.
Контекст: FTE-03 добавил порт и тест в internal/infra/mcp/tool_stats.go. Обработчик публикует снимок гейта только когда порт привязан. Адаптер живёт в композитном корне, а эти файлы не входят в OwnerFiles карточки FTE-03.
Шаги: Дождаться мержа PR 1906; добавить поле SharedGateSnapshotReader в mcp.Dependencies; построить адаптер поверх store.SharedCIWaitSnapshot и привязать его в attachStats; вызвать `beacon deferred done` для записи с watchID из SourceWatch.
Проверка: beacon_stats без привязанного порта сохраняет прежний ответ; с привязанным портом ответ содержит activeWaiters и combinedRounds; локальный гейт WIRING-* проходит.
Приёмка: Проводка применена, запись в `beacon deferred --format json` имеет status: done, CI зелёный.

## Причина

OwnerFiles карточки FTE-03 ограничены internal/infra/mcp/tool_stats.go и internal/infra/mcp/tool_stats_test.go. Правило слоёв ADR 0005 запрещает обработчику импортировать internal/infra/store, поэтому адаптер должен появиться в композитном корне, который принадлежит другим карточкам.

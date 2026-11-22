# ATX-44. Публиковать active_watches и outbox_depth
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/observe/metrics.go, internal/infra/mcp/tool_stats.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Показывать реальное число вотчей и глубину очереди в /metrics.
Контекст: Источник: docs/research/metrics-audit.md, строка таблицы 117 и раздел «Почему два gauge всегда нулевые». internal/infra/observe/metrics.go:49-50 объявляет gauge, хелперы ActiveWatches и OutboxDepth в :81 и :84 вызываются только в cmd/beacon/observe_test.go:134-135 и :182-183. В демоне и MCP публикаторов нет. Эндпоинт /metrics отдаёт beacon_active_watches 0 и beacon_outbox_depth 0 при любом числе вотчей. beacon_stats в internal/infra/mcp/tool_stats.go:301 читает устойчивые счётчики store, но не observe.Metrics. Baseline: оба gauge всегда 0.
Шаги: Вызывать ActiveWatches и OutboxDepth в демоне и MCP на каждом проходе. Связать значения с источниками, которые уже использует beacon_stats, чтобы числа не расходились.
Проверка: CI зелёный. Тест с активной вотчей видит beacon_active_watches 1 в /metrics. Значения совпадают с beacon_stats.
Приёмка: Gauge отражают реальное состояние. Расхождение между /metrics и beacon_stats исключено.

Итог: Already landed in PR #1396 (commit 19ca1996b55d2cc54e077f002796a2053d347dda): fix(metrics): publish active_watches and outbox_depth from beacon_stats (ATX-44) (#1396)
Доказательство: PR #1396; commit 19ca1996b55d2cc54e077f002796a2053d347dda; git merge-base --is-ancestor 19ca1996b55d2cc54e077f002796a2053d347dda origin/main exit 0; no duplicate code PR needed.
Причина: already landed in PR #1396 19ca1996b55d
Время закрытия: 2026-11-22T11:27:01Z

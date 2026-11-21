# RS2-MCP-06. Срок жизни watchID как handle
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/domain/fanout.go, internal/domain/types.go, internal/domain/transition.go, internal/infra/mcp/tool_wait_result.go, internal/infra/mcp/tool_claim.go
Зависимости: 
Ссылка: PR #882
Агент: нет
Время: нет
Цель: Добавить время выпуска в watchID и инструментальную ошибку для просроченного handle.
Контекст: Premise: RS-MCP-06 вернула блокер, watchID строится в fanout.go без времени, ADR 0019 снял TTL closure.
Шаги: Добавить issuedAt и TTL, вернуть isError для истёкшего handle, описать срок в схеме, тесты на живой и мёртвый handle.
Проверка: gofmt, go test в CI.
Приёмка: Обращение к мёртвому handle не выглядит пустым результатом.

## Подтверждение
- 2026-11-21, origin/main 17dfe44. PR #882 (https://github.com/refusedguy/beacon/pull/882, merge commit 669a8a5) влит. `grep -n 'WatchHandleTTL' internal/domain/fanout.go` = 29, `grep -n 'CodeWatchExpired' internal/infra/mcp/tool_wait_result.go` = 1140, `grep -n 'leased handle valid' internal/infra/mcp/tool_wait_result.go` = 1403. watchID несёт время выпуска и аренду 30 минут, истёкший handle даёт WATCH_EXPIRED до чтения хранилища. Тесты покрывают живой, истёкший и устаревший handle. Блокер снят. Код не писал, gofmt не применял, OwnerFiles не менял, wiring и .beacon не трогал.

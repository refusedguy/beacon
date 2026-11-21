# RS2-MCP-06. Срок жизни watchID как handle
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/domain/fanout.go, internal/domain/types.go, internal/domain/transition.go, internal/infra/mcp/tool_wait_result.go, internal/infra/mcp/tool_claim.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Добавить время выпуска в watchID и инструментальную ошибку для просроченного handle.
Контекст: Premise: RS-MCP-06 вернула блокер, watchID строится в fanout.go без времени, ADR 0019 снял TTL closure.
Шаги: Добавить issuedAt и TTL, вернуть isError для истёкшего handle, описать срок в схеме, тесты на живой и мёртвый handle.
Проверка: gofmt, go test в CI.
Приёмка: Обращение к мёртвому handle не выглядит пустым результатом.

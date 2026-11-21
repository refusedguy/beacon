# ARDH-14. Разделить жирные порты Store и BlockerStore
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/domain/types.go, internal/infra/store/, cmd/beacon/mcp.go
Зависимости: 
Ссылка: ADR 0008
Агент: нет
Время: нет
Цель: Разбить domain.Store на WatchStore и ObligationStore, а BlockerStore на порт инцидентов и порт привязок, собрав их в агрегат только в composition root.
Контекст: Источник: docs/research/solid-gof-audit.md, ось ISP. Store имеет 7 методов, BlockerStore 8, blockerControl в bridge.go 5. Узкие порты уже приняты в проекте и работают. Риск: средний. Правка затронет много реализаций и wiring в cmd/beacon.
Шаги: Определить новые порты рядом с потребителями. Обновить реализации store и адаптеры в cmd/beacon. Сохранить JSON-контракты MCP.
Проверка: layers и capabilities гейты зелёные в CI,/tools/list без изменений.
Приёмка: Каждый порт содержит методы одной сущности, реализации собираются в composition root.

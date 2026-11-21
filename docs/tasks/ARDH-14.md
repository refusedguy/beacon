# ARDH-14. Разделить жирные порты Store и BlockerStore
Статус: done
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

## Подтверждение
- Реализация в PR [#1176](https://github.com/refusedguy/beacon/pull/1176). `domain.Store` разделён на `WatchStore` (4 метода Watch) и `ObligationStore` (3 метода Obligation). `domain.BlockerStore` разделён на `BlockerIncidentStore` (5 методов инцидентов) и `OrchestratorBindingStore` (3 метода привязок). Неиспользуемый алиас `ControlPlaneStore` удалён: ссылок на него в `internal`, `cmd`, `tools` и `tests` не было.
- Агрегат живёт только на стороне сборки. `domain.Fanout`, `cibridge.Bridge` и `mcp.StateStore`/`WatchPRHandler` объявляют локальный агрегат из двух узких портов; конкретный `*store.DB` создаётся один раз в `cmd/beacon` (`openProcessStore`/`buildMCPDependencies`) и передаётся всем потребителям. Комментарии и проверки `var _` в `internal/infra/store` обновлены под четыре порта.
- Поведение сохранено: тела методов, JSON-контракты MCP и `/tools/list` не менялись. Затронуты только объявления интерфейсов, типы полей и параметров, проверки реализаций и комментарии. `gofmt` применён ко всем 17 Go-файлам правки. `go test`, `go build` и гейты оставлены CI по правилам репозитория.

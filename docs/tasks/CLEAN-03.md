# CLEAN-03. Удалить мёртвые порты mcp.Bridge, Notifier и ControlPlaneStore
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/server.go, internal/infra/mcp/tool_watch_pr.go, internal/infra/mcp/tool_watch_pr_test.go, internal/domain/types.go, internal/domain/types_test.go
Зависимости: 
Ссылка: ADR 0086, ADR 0094
Агент: нет
Время: нет
Цель: Удалить неиспользуемые точки расширения второго протокола: mcp.Bridge и BridgeRequest, WithBridges с циклом рассылки, domain.ControlPlaneStore и domain.Notifier.
Контекст: Источник: docs/research/solid-gof-audit.md, раздел про лишние слои, и docs/research/cleanup-plan.md, задача 4. WithBridges вызывается только в tool_watch_pr_test.go:702, Notifier только в types_test.go:42 и 175-178. ADR 0086 и 0094 запрещают второй протокол. Риск: низкий. Около 150-220 строк вместе с тестами.
Шаги: Удалить объявления и ветки рассылки, убрать тесты, которые держали только эти символы. Проверить, что mcp.NewServerWithDeps и восемь инструментов не изменились.
Проверка: CI зелёный. tools/list contract test проходит без обновления золотых значений.
Приёмка: Символов Bridge, BridgeRequest, WithBridges, ControlPlaneStore и Notifier нет в продакшен-коде и тестах. Контракт из восьми инструментов не изменился.

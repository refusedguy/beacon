# CLEAN-03. Удалить мёртвые порты mcp.Bridge, Notifier и ControlPlaneStore
Статус: done
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

## Подтверждение

PR: https://github.com/refusedguy/beacon/pull/1157

Проверка до удаления выполнена с помощью rg по каталогам internal, cmd и tools:

- Bridge, BridgeRequest, BridgeReply и WithBridges найдены только в mcp-порту и его тестовом дубле. cibridge.Bridge и tools/beacon-gates/reachability оставлены, это живые символы.
- ControlPlaneStore имел только объявление в internal/domain/types.go, использований нет.
- domain.Notifier встречался только в internal/domain/types_test.go. app/tasks.Notifier и app/session.Notifier оставлены.

Удалены mcp.Bridge, BridgeRequest, BridgeReply, WithBridges, цикл рассылки startBridges, domain.ControlPlaneStore, domain.Notifier и тесты этих символов. ToolNames и NewServerWithDeps не менялись. gofmt выполнен по пяти файлам. Сборку и тесты проверяет CI.

База fix/reports-to-tasks была влита в main как PR #1152 и удалена, поэтому PR открыт в main.

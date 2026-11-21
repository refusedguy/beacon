# CLEAN-15. Подключить или удалить registryWakeAllowed
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/registry_sync.go, cmd/beacon/registry_sync_test.go, docs/tasks/T5.49.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Либо подключить guard к реальному вызову registry sync, либо удалить функцию и комментарий о потребителе T5.49.
Контекст: Источник: docs/research/deadcode-audit.md, строка про registryWakeAllowed. Единственный вызов в registry_sync_test.go:250. Комментарий называет потребителем T5.49, но карточка зафиксировала отсутствие вызова вне теста. Риск: средний.
Шаги: Прочитать T5.49 и путь registry sync. Выбрать вариант по факту: подключить проверку в продуктивный вызов или удалить функцию вместе с тестом и комментариями.
Проверка: CI зелёный. Нет комментария, обещающего несуществующего потребителя.
Приёмка: Функция либо используется в продуктивном коде, либо удалена. Тесты соответствуют выбранному варианту.

## Подтверждение

Проверено по origin/main (b9937c01) 2026-11-21. Premise из docs/research/deadcode-audit.md:32 устарела: функция вызывается не только в тесте. Guard уже подключён к реальному wake-пути, удалять его нельзя. Кодовых изменений нет.

Цепочка в продуктивном коде:

- cmd/beacon/registry_sync.go:388. wakeRegistryGuard.WakeStatus вызывает registryWakeAllowed(status) и разрешает wake только при status=active.
- cmd/beacon/registry_sync.go:379. newWakeRegistryGuard создаёт адаптер, реализующий mcp.RegistryWakeGuard.
- cmd/beacon/mcp.go:448. newMCPWakeHandler подключает адаптер через WithRegistryGuard к продакшн-хендлеру, когда deps.Store равен *store.DB.
- internal/infra/mcp/tool_wake.go:658,1775. WakeHandler хранит порт RegistryWakeGuard и проверяет его перед wake.

Карточки в main: T5.49 со статусом done и PR #1112, T5.55 со статусом done, T5.61 со статусом done. Комментарии в registry_sync.go и mcp.go описывают реального потребителя, удалять их не нужно.

PR: <!--PR-->

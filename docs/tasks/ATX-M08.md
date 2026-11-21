# ATX-M08. Свести три замка карточки к одному
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/tool_task_claim.go, internal/infra/mcp/tool_task_close.go, cmd/beacon/task.go
Зависимости: 
Ссылка: docs/research/mcp-close-chain-gaps.md, finding M08
Агент: нет
Время: нет
Цель: Устранить гонку между выдачей карточки, закрытием через MCP и закрытием через CLI.
Контекст: Источник: docs/research/mcp-close-chain-gaps.md, finding M08. Claim держит `.beacon-task-claim.lock` (`internal/infra/mcp/tool_task_claim.go:312-317`), MCP close держит `.beacon-task-close.lock` (`internal/infra/mcp/tool_task_close.go:323-329`), CLI использует `taskstore.LockPath` (`cmd/beacon/task.go:549`). Claim пишет поле `Агент` под своим замком (`internal/infra/mcp/tool_task_claim.go:286`), close проверяет его под другим. Baseline: 3 разных файла блокировок для одного файла карточки.
Шаги: Определить один канонический путь замка для всех трёх операций. Передать его в `FileTaskClaimStore`, `FileTaskCloseStore` и CLI-конфигурацию. Сохранить совместимость со старыми файлами блокировок или явно объявить их устаревшими. Добавить тест с параллельными claim и close на одной карточке. Проверить, что владелец `in_progress` не может измениться между проверкой и записью.
Проверка: Тесты зелёные в CI. Параллельный тест проходит без простоя и без двойного заполнения поля `Агент`.
Приёмка: Все три операции используют один путь замка. Проверка владельца выполняется внутри критической секции, которая пишет статус.

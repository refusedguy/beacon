# IDX-05. MCP инструмент beacon_task_claim
Premise: проверено 2026-11-21 по origin/main. Реализация уже влита PR #903, merge commit b4eb1d3. Файлы `internal/infra/mcp/tool_task_claim.go` и `tool_task_claim_test.go` на месте, дубля нет. Регистрация имени и схемы сделана отдельно в IDX-06 через PR #913 и ADR 0268, эту работу карточка не повторяет. Тесты `TestFileTaskClaimRejectsSecondAgent`, `TestFileTaskClaimConcurrentAgentsGetDifferentCards` и `TestTaskClaimHandlerReturnsFullContext` закрывают приёмку. Правка только текстовая: статус карточки переведён в done, код не менялся.
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/tool_task_claim.go, internal/infra/mcp/tool_task_claim_test.go
Зависимости: 
Ссылка: 
Агент: ses_e02a3609dffeNMDaGo12powbN5
Время: 2026-11-21T09:16:57Z
baseline: 969 карточек в docs/tasks, 91 со статусом todo, 0 с полем «Режим плана». Замер 2026-11-21, find и grep по docs/tasks.
Решение: Порт TaskClaimStore и адаптер FileTaskClaimStore на стандартной библиотеке. Адаптер держит исключительный flock во время выбора и записи, перечитывает карточки под замком и меняет статус через atomic rename. Второй агент получает GONE. При отсутствии поля «Режим плана» возвращается fast. Регистрация имени и схема остаются в IDX-06.
Цель: Атомарная выдача задачи сабенту с полным контекстом.
Контекст: Premise: пайплайн владельца требует тулзу: оркестратор заспавнил агента, агент дёрнул тулзу и получил задачу.
Шаги: Вход с agent ID, возврат ID, цели, зависимостей, OwnerFiles, режима плана и handoff, пометка in_progress, защита от двойной выдачи.
Проверка: gofmt, go test в CI.
Приёмка: Два агента не получают одну карточку, ответ содержит все поля.

## Подтверждение
- PR #903: https://github.com/refusedguy/beacon/pull/903, merge commit b4eb1d380a0fcee82d24fdb99a2a8c36492d84d6. Файлы: `internal/infra/mcp/tool_task_claim.go`, `internal/infra/mcp/tool_task_claim_test.go`, `docs/tasks/IDX-05.md`.
- `FileTaskClaimStore.Claim` держит исключительный flock на время выбора и записи, перечитывает карточки под замком и меняет статус через atomic rename. Второй агент получает `CodeGone`.
- `TaskClaimOutput` несёт id, заголовок, цель, зависимости, OwnerFiles, режим плана, время и пятипольный handoff. `TestTaskClaimHandlerReturnsFullContext` проверяет полноту ответа, `TestFileTaskClaimRejectsSecondAgent` и `TestFileTaskClaimConcurrentAgentsGetDifferentCards` проверяют атомарность.
- gofmt и go test запускает CI на PR. Дубль IDX-06 не создавался.

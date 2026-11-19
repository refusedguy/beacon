# ADR 0028. Идемпотентность, квитанция и прерывание ожидания для ночной смены

Статус: accepted

## Решение

Универсальный night-shift manager соблюдает три правила контракта. Правила не привязаны к конкретному продукту и работают через один мост на протокол из ADR 0024.

1. Идемпотентность по `clientRequestId`. Повтор `wait_result` или `steer` после обрыва связи с тем же `clientRequestId` не создаёт вторую задачу. Повтор возвращает результат первой операции: ту же задачу, ту же квитанцию и то же место в очереди. Новый `clientRequestId` создаёт новую задачу.
2. Квитанция означает записанное намерение, а не готовый результат. Ответ `ack` подтверждает, что команда принята в хранилище и будет выполнена. Он не подтверждает, что агент закончил. Готовый вердикт читается отдельно через `get_result` или `wait_result`.
3. Ожидание прерывается, когда агенту нужен человек. Флаги `hasPendingQuestion` и `pendingApproval` заставляют `wait_result` вернуться раньше таймаута с причиной прерывания. После ответа человека ожидание продолжается по лестнице из ADR 0017.

## Причина

Отчёт N «Фичи которые стоит взять» называет эти три правила главным, что стоит взять у конкурентов ([N, пункты 2, 3 и 5](https://github.com/pingdotgg/t3code/blob/main/docs/internals/overview.md#L45-L59)). Повтор запроса не должен создавать вторую работу. Идемпотентность по `clientRequestId` реализована в [aresyn](https://github.com/aresyn/codex-control-plane-mcp/blob/main/README.md#L54-L62) и [t3code](https://github.com/pingdotgg/t3code/blob/main/docs/orchestration-v2/orchestrator-mcp-server.md#L445-L447). Транзакционная модель квитанции описана в [t3code](https://github.com/pingdotgg/t3code/blob/main/docs/internals/overview.md#L45-L59). Флаги `hasPendingQuestion` и `pendingApproval` описаны в [t3code](https://github.com/pingdotgg/t3code/blob/main/docs/orchestration-v2/orchestrator-mcp-server.md#L280-L292) и прямо решают проблему зависшего `wait_result`.

Отчёт O «Фичи для Beacon» добавляет две причины. Сценарий с перезапуском продолжает работу с сохранённых данных после сбоя, поэтому повтор запроса после обрыва обязан находить прежнюю задачу ([workflows](https://code.claude.com/docs/en/workflows.md)). Рантайм не останавливается сам для ввода человека, поэтому согласие человека выносится в отдельный прогон, а менеджер должен уметь прервать ожидание по флагу ([workflows](https://code.claude.com/docs/en/workflows.md)).

ADR 0014 описал квитанции адаптера, но не уточнил, что `ack` означает. ADR 0026 потребовал честной доставки follow-up. ADR 0020 требует, чтобы задача приходила из согласованного решения, поэтому карточки появятся только после статуса `accepted`.

## Последствия

- `clientRequestId` становится обязательным полем `wait_result`, `steer` и `ack`. Хранилище ведёт индекс «идентификатор запроса — операция» и пишет его в одной транзакции с состоянием по ADR 0015.
- Ответ `ack` содержит записанное намерение. Терминальный вердикт по-прежнему приходит только из `get_result` или `wait_result`.
- `wait_result` возвращается по `hasPendingQuestion` или `pendingApproval` до истечения таймаута и называет причину. Прерывание не списывает попытку лестницы из ADR 0017.
- Из каждого пункта выходит одна небольшая карточка в `docs/tasks/` по правилам ADR 0020. Карточки не создаются этим ADR и будут созданы отдельными задачами.
  - Идемпотентность: `internal/infra/mcp/server.go`, `internal/infra/mcp/tool_wait_result.go`, `internal/infra/mcp/tool_ack.go`, `internal/infra/store/receipts.go`, `internal/infra/store/db.go`, `internal/domain/identity.go` и тесты к ним.
  - Контракт `ack`: `internal/infra/mcp/tool_ack.go`, `internal/infra/mcp/schema.go`, `internal/domain/types.go`, `internal/infra/store/receipts.go` и тесты к ним.
  - Прерывание ожидания: `internal/infra/mcp/tool_wait_result.go`, `internal/infra/mcp/tool_get_result.go`, `internal/infra/ladder/escalation.go`, `internal/domain/types.go` и тесты к ним.
- Проверка карточек: повтор запроса после обрыва не создаёт вторую задачу; `ack` возвращается до завершения агента, а вердикт приходит из `get_result`; `wait_result` возвращается по флагу раньше таймаута.

## Отклонённое

Использовать текст сообщения или содержание задачи как ключ идемпотентности. Два одинаковых сообщения могут быть двумя разными задачами.

Возвращать результат агента в ответе `ack`. Это смешивает записанное намерение с вердиктом и ломает честную доставку из ADR 0026.

Ждать таймаута `wait_result` и опрашивать флаги на стороне клиента. Это оставляет ожидание зависшим и перекладывает работу менеджера на клиента.

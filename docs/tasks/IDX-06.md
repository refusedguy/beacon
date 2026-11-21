# IDX-06. Схема и регистрация claim инструмента
Premise: проверено 2026-11-21 по origin/main. ADR 0094 (docs/adrs/0094-seven-tools.md, раздел «Приёмка») прямо требует отдельного решения или ADR на девятый инструмент. Контракт в `internal/infra/mcp/contract_test.go` имел ровно восемь имён в `expectedToolNames`, восемь записей в `toolsListOrder` и восемь в `expectedToolKinds`; золотые константы описывали ответ `tools/list` для восьми инструментов. Инструмент `beacon_task_claim` в реестре отсутствовал. Реализация обработчика принадлежит IDX-05 (`tool_task_claim.go`, `tool_task_claim_test.go`) и в этой карточке не дублируется.
baseline: `git show origin/main:docs/adrs/0094-seven-tools.md | grep -n 'Девятый инструмент требует отдельного решения или ADR'` = строка приёмки; `git show origin/main:internal/infra/mcp/contract_test.go | grep -c 'beacon_'` = 24 упоминания восьми имён до правки; `grep -n 'toolsListByteCount' internal/infra/mcp/contract_test.go` = 16336 байт и SHA-256 `315a91fb62d576668aaecf4f1c2b3caaee3f06347c52edf4a771d74ab2c9ddd1` для восьми инструментов; `grep -n 'ToolNames' internal/infra/mcp/server.go` = список из восьми имён; бюджет `toolsListByteBudget` = 16 * 1024 байт.
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/schema.go, internal/infra/mcp/server.go, internal/infra/mcp/contract_test.go
Зависимости: 
Ссылка: ADR 0268
Агент: нет
Время: нет
Цель: Зарегистрировать девятый инструмент по отдельному ADR и зафиксировать его схему.
Контекст: Premise: ADR 0094 требует отдельного ADR на девятый инструмент. Контракт проверяет имена, аннотации и бюджет 16 KiB.
Шаги: Написать ADR, добавить имя в реестр, схему вывода, обновить золотые константы в том же PR.
Проверка: gofmt, go test в CI.
Приёмка: Контракт зелёный, ADR принят, девять имён зафиксированы.

## Подтверждение
- Принят ADR 0268 в `docs/adrs/0268-task-claim-tool.md`: отдельное имя `beacon_task_claim`, аддитивная аннотация без идемпотентности, схема вывода из типа IDX-05.
- В `schema.go` добавлена фабрика `taskClaimOutputSchema` для типа `TaskClaimOutput` из `tool_task_claim.go`. Тип не дублируется. Фабрика допускает дополнительные поля в ответе.
- В `server.go` имя добавлено в `ToolNames` и `toolAnnotations`, порт `TaskClaimStore` добавлен в `Dependencies`. Хелпер `attachTaskClaim` подключает обработчик IDX-05 с явной схемой вывода.
- В `contract_test.go` обновлены `expectedToolNames`, `toolsListOrder`, `expectedToolKinds`, а также золотые константы размера и SHA-256. Список содержит девять имён, бюджет остаётся 16 KiB. Замер ответа `tools/list`: 17922 байт, SHA-256 `cc1b166a25af2c69e2f6dfe4fb9f5d22b9996b2712f6bc971eb32abd24005266`.
- gofmt применён к трём Go-файлам карточки. go test и go build локально не запускались, проверки проходят в CI на PR. Статус карточки меняет Beacon при мерже.

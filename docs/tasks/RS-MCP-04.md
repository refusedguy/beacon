# RS-MCP-04. Идемпотентность и состояние outcome_unknown
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/domain/types.go, internal/infra/store/db.go, internal/infra/store/db_test.go
Зависимости: 
Ссылка: 
Агент: subagent t17 (fix/t17)
Время: 2026-11-21
Цель: Реализовать протокол из ADR по идемпотентности: operation_id, квитанция и эффект в одной транзакции, состояние outcome_unknown и tombstone вместо повторного действия.
Контекст: Premise: проверено 2026-11-21,claimedAt в beacon_ack и идемпотентность beacon_defer_wiring есть, но явного состояния outcome_unknown нет. baseline: `grep -rn 'outcome_unknown\|OperationID' internal/domain internal/infra/store` пусто. Источник: mcpruntime gateway idempotency ADR и SQLite пример с BEGIN IMMEDIATE.
Шаги: Добавить тип ключа операции и поле operation_id в доменные типы. Добавить в store метод записи квитанции и эффекта в одной транзакции с уникальным ключом. Различать Created, Duplicate, Conflict и InFlight. После таймаута или 5xx писать tombstone failed-unknown. Тесты на параллельные повторы, потерянный ответ и конфликт параметров.
Проверка: gofmt чистый. go test в CI. Негативный контроль: повтор с другим телом даёт конфликт, а не тихий success.
Приёмка: Повтор после неопределённого исхода не запускает второе действие, а возвращает operation_id и next_action. Запись и квитанция не расходятся при падении между шагами.

## Реализация (2026-11-21)

- `internal/domain/types.go`: `OperationID`, состояния `accepted/running/succeeded/failed/outcome_unknown`, `OperationReceipt` с полем `operation_id`, `next_action`, `IsTombstone`, `ObservedState`, `ErrIndeterminateOutcome`/`IsIndeterminate`. JSON-RPC id не используется как ключ.
- `internal/infra/store/db.go`: таблица `operation_receipts` с уникальным namespaced-ключом, квитанция и эффект пишутся в одной транзакции через `ExecuteOperation`. Различаются `Created`, `Duplicate`, `Conflict` (другое тело), `InFlight` (accepted/running) и `OutcomeUnknown` (tombstone). `MarkOutcomeUnknown` пишет tombstone `failed/unknown` после таймаута или 5xx; повтор возвращает `outcome_unknown` и `next_action=wait_for_human`, не запуская действие. Добавлены `BeginOperation`, `StartOperation`, `Operation`, `OperationCount`. Результат и параметры хэшируются/редктируются.
- `internal/infra/store/db_test.go`: параллельные повторы (ровно один эффект), потерянный ответ (возврат сохранённого результата), конфликт параметров, indeterminate-эффект и tombstone, явный `MarkOutcomeUnknown`, переходы состояний, definite failure, редукция и изоляция ключей, негативные проверки.

Проверка в рамках карточки: `gofmt` чистый. `go test`/`go build` по инструкции не запускались, прогон в CI. Чужие OwnerFiles, wiring/ и .beacon/ не тронуты.

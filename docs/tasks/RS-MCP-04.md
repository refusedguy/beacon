# RS-MCP-04. Идемпотентность и состояние outcome_unknown
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/domain/types.go, internal/infra/store/db.go, internal/infra/store/db_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Реализовать протокол из ADR по идемпотентности: operation_id, квитанция и эффект в одной транзакции, состояние outcome_unknown и tombstone вместо повторного действия.
Контекст: Premise: проверено 2026-11-21,claimedAt в beacon_ack и идемпотентность beacon_defer_wiring есть, но явного состояния outcome_unknown нет. baseline: `grep -rn 'outcome_unknown\|OperationID' internal/domain internal/infra/store` пусто. Источник: mcpruntime gateway idempotency ADR и SQLite пример с BEGIN IMMEDIATE.
Шаги: Добавить тип ключа операции и поле operation_id в доменные типы. Добавить в store метод записи квитанции и эффекта в одной транзакции с уникальным ключом. Различать Created, Duplicate, Conflict и InFlight. После таймаута или 5xx писать tombstone failed-unknown. Тесты на параллельные повторы, потерянный ответ и конфликт параметров.
Проверка: gofmt чистый. go test в CI. Негативный контроль: повтор с другим телом даёт конфликт, а не тихий success.
Приёмка: Повтор после неопределённого исхода не запускает второе действие, а возвращает operation_id и next_action. Запись и квитанция не расходятся при падении между шагами.

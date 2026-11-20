# BS.37. /api/v1 wake и вопросы
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/server/api_wake.go
Зависимости: BS.34, BS.22
Ссылка: ADR 0198
Агент: нет
Время: нет
Цель: TUI может отвечать на вопросы и инициировать wake.
Контекст: Crush имеет /permissions/grant, /questions/answer и /questions/cancel.
Шаги: Команды wake, question answer, approval respond с clientRequestId.
Проверка: Повтор команды возвращает прежний receipt.
Приёмка: Привилегированные действия пишут аудит.

## Подтверждение
- PREMISE проверен: функционал уже в origin/main, код не дублирован. Коммит 6fdb2c4 (#474) добавил `internal/infra/server/api_wake.go` с тремя привилегированными командами: wake (`/api/v1/wake`), question answer (`/api/v1/questions/answer`) и approval respond (`/api/v1/approvals/respond`). Каждая несёт `clientRequestId` (ADR 0028); `CommandProcessor` хранит receipt по ключу и на повторе возвращает его с `Replayed=true`, не вызывая порт повторно. Аудит пишется через `AuditWriter` действиями `wake`, `question.answer`, `approval.respond`, идемпотентность и аудит покрыты в `api_wake_test.go`. Статус done проставлен коммитом 5574278 (#533). OwnerFiles уже содержат контракт, новый код создал бы вторую копию.

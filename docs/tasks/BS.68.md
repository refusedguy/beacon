# BS.68. Сервис вопросов и одобрений
Статус: dropped
Фаза: P5
Тип: task
Владелец файлов: internal/app/session/pending.go
Зависимости: BS.37
Ссылка: ADR 0199
Агент: нет
Время: нет
Цель: Wait прерывается, когда нужен человек.
Контекст: T3 pending_request end'ит wait с hasPendingQuestion или pendingApproval.
Шаги: Типы question и approval, expires_at, list/read/respond/cancel.
Проверка: Ответ до старта очевидного turn отменяет этот turn.
Приёмка: Одобрения не маршрутизируются как вопросы.

## Подтверждение
- PR #394 DROPPED: Pending question/approval service from old T3 session architecture; current flow uses beacon_ack blocked + wake text, not a pending_request service.

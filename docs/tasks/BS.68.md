# BS.68. Сервис вопросов и одобрений
Статус: todo
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

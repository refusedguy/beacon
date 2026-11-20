# BS.65. Напоминания о дедлайнах и snooze
Статус: dropped
Фаза: P5
Тип: task
Владелец файлов: internal/app/tasks/reminders.go
Зависимости: BS.31, BS.29
Ссылка: ADR 0199
Агент: нет
Время: нет
Цель: Задача будит только когда нужно.
Контекст: T3 snoozed thread просыпается только при pending request или failure.
Шаги: snoozedUntil, reason, отправка только по разрешённым причинам.
Проверка: Completion wake не пробуждает snoozed задачу.
Приёмка: Позднее событие не меняет терминальный статус.

## Подтверждение
- PR #394 DROPPED: snoozedUntil/reminder service has no implementation; terminal guard and blockedNotified already stop loops; dead T3 session concept.

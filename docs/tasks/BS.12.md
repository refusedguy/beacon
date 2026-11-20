# BS.12. Outbox в транзакции команды
Статус: done
Обоснование: реализация уже в main (commit 0a830e9, PR #447; internal/app/command/processor.go, internal/infra/store/outbox.go), карта уже в статусе done. Дубля в OwnerFiles нет, код не писался.
Фаза: P1
Тип: task
Владелец файлов: internal/app/command/processor.go, internal/infra/store/outbox.go
Зависимости: BS.11
Ссылка: ADR 0190
Агент: нет
Время: нет
Цель: Намерение эффекта не может опередить коммит.
Контекст: T3 EventSink фиксирует события, проекции, receipt и outbox одной транзакцией.
Шаги: CommandProcessor без внешнего I/O, запись outbox в той же транзакции.
Проверка: Откат транзакции не оставляет outbox.
Приёмка: Эффект выполняется только после коммита.

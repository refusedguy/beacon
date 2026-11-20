# BS.16. EventBus с доставкой после коммита
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/app/event/bus.go
Зависимости: BS.11
Ссылка: ADR 0190
Агент: нет
Время: нет
Цель: Подписчик не видит событие до коммита.
Контекст: openrig event-bus использует notify envelope и discardOnThrow.
Шаги: In-process подписчики, envelope в транзакции, дренаж после коммита, savepoint.
Проверка: Ошибочный писатель не отравляет шину.
Приёмка: Есть watermark для отравленного события.
- Premise проверен: функциональность уже в main. In-process EventBus с доставкой после коммита влит коммитом 7494865 (PR #445): envelope в транзакции, дренаж после коммита, savepoint (Tx.Savepoint/RollbackTo), изоляция ошибочного писателя и watermark для отравленного события (Bus.Watermark, LastPoison, PoisonError) в internal/app/event/bus.go с тестами в bus_test.go. OwnerFiles менять нечего, дубль не создан.

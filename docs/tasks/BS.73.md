# BS.73. Scenario harness с stub-исполнителями
Статус: todo
Фаза: P6
Тип: task
Владелец файлов: internal/testsystem/harness.go
Зависимости: BS.14
Ссылка: ADR 0191
Агент: нет
Время: нет
Цель: Сценарии проверяются без внешнего I/O.
Контекст: У openrig 15 library scenarios и stub-agent.
Шаги: Stub runner, детерминированные события, seed regression.
Проверка: Сценарий проходит без GitHub и провайдеров.
Приёмка: Готовые сценарии переиспользуются в eval.

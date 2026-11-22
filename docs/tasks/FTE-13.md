# FTE-13. Помечать orphan wake в доставке и метрике
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/cibridge/wake_dispatcher.go, internal/infra/observe/metrics.go, internal/infra/observe/observe.go
Зависимости:
Ссылка: docs/evals/e05-orphan-wake-flagged.md:733-743, internal/infra/cibridge/wake_dispatcher.go:596
Агент: нет
Время: нет
baseline: markOrphaned в internal/infra/cibridge/wake_dispatcher.go:596 существует и пишет событие executor_orphaned. git grep не находит orphan_wake_total или поля orphan в wake_delivered, 0 совпадений.
Цель: Доставка wake по несуществующей сессии помечается как orphan в логе и в отдельном счётчике.
Контекст: docs/evals/e05-orphan-wake-flagged.md:737 требует orphan=true в wake_delivered. Строка 738 требует увеличенный orphan_wake_total. Сейчас несуществующая сессия приводит к WorkerPhaseOrphaned и событию executor_orphaned, но метрики и поле доставки этого не отражают. Оператор видит orphan только по косвенным признакам.
Шаги: Добавить счётчик orphan_wake_total в internal/infra/observe/metrics.go и функцию записи в observe.go. Вызывать её из markOrphaned и из пути доставки, который находит мёртвую сессию. Добавить отдельное поле orphan в WakeDelivery без выноса sessionID в метрики. Покрыть тестом, что живая сессия из active не помечается.
Проверка: Событие wake_delivered для мёртвой сессии содержит orphan=true. orphan_wake_total увеличен на один. Живая сессия не помечается. В именах и labels метрик нет sessionID.
Приёмка: Eval e05 проходит, CI зелёный.

# ATX-33. Не будить orphaned подписчиков
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/infra/wake/deliver.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Не тратить доставку на подписчика с завершённой сессией.
Контекст: Источник: docs/research/watch-lifecycle-audit.md, находка 7 и строка таблицы 119. wake_dispatcher.go:307-309 и :391-458 помечают подписчика orphaned при мёртвой сессии. publishActionable в verdict_applier.go:144-160 не проверяет фазу подписчика, поэтому orphaned продолжает получать wake на каждой новой ревизии. На main эта логика живёт в internal/infra/cibridge/bridge.go. Приоритет P1 в отчёте.
Шаги: Проверять WorkerPhaseOrphaned в publishActionable до вызова wakeOne. Не списывать бюджет wake на orphaned подписчика.
Проверка: CI зелёный. Тест с orphaned подписчиком и новой ревизией не создаёт доставку для него.
Приёмка: Orphaned подписчики исключены из рассылки. Бюджет wake расходуется только на активных.

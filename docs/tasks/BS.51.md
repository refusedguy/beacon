# BS.51. Контроль footprint меньше 100 MB
Статус: done
Обоснование: реализация уже в main (commit 7d2d707, PR #494; internal/infra/server/metrics.go содержит GaugeRSSBytes/beacon_rss_bytes, константы FootprintWatchThreshold=15 и FootprintLimitBytes=100 MB, функцию FootprintCheck с WARNING при превышении бюджета и тест TestFootprintCheckAtFifteenWatches). Карта уже в статусе done. Дубля в OwnerFiles нет, код не писался.
Фаза: P4
Тип: task
Владелец файлов: internal/infra/server/metrics.go, internal/infra/store/observability.go
Зависимости: BS.46
Ссылка: ADR 0191
Агент: нет
Время: нет
Цель: Сервер укладывается в ограничение владельца.
Контекст: Цель меньше 100 MB RSS при 15 и более агентах.
Шаги: Метрика RSS, предупреждение при превышении, тест на 15 вотчей.
Проверка: Замер при 15 вотчах не превышает порог.
Приёмка: Рост числа процессов gh равен нулю.

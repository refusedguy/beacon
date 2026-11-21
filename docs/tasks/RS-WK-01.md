# RS-WK-01. Per-adapter circuit breaker для доставки wake
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/infra/cibridge/bridge_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Добавить защиту от сломанного адаптера: скользящее окно, минимальное число попыток, OPEN с задержкой и HALF_OPEN с одной пробой.
Контекст: Premise: проверено 2026-11-21, wake cap в шесть попыток переживает рестарт, но это одноразовый предел без возврата. baseline: `grep -n 'wakeCount\|wakeCap' internal/infra/cibridge/bridge.go` находит счётчик, `grep -rn 'HALF_OPEN\|halfOpen' internal/infra/cibridge` пусто. Источник: Resilience4j CircuitBreaker.
Шаги: Завести состояние на адаптер в точке wakeOne. Считать только ошибки транспорта, dead session не считать сбоем backend. Добавить minimumNumberOfCalls, чтобы breaker не открывался на одной ошибке. Отдельно отметить, что скользящее окно не ограничивает параллелизм. Тесты на OPEN, HALF_OPEN и ignoreExceptions.
Проверка: gofmt чистый. Тесты в CI. Wake cap на Watch не отменяется.
Приёмка: Сломанный хост не вызывает повторную доставку каждого тика. Одна проба в HALF_OPEN возвращает адаптер в работу.

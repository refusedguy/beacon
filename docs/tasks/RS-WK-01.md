# RS-WK-01. Per-adapter circuit breaker для доставки wake
Premise: проверено 2026-11-21 по origin/main (6ece091). wake cap в шесть попыток живёт в obligations и переживает рестарт, но это одноразовый предел без возврата: после шести wake Watch паркуется. Пер-адаптерного breaker с OPEN и HALF_OPEN в main нет. Источник паттерна: Resilience4j CircuitBreaker. Состояние breaker хранится в памяти Bridge в bridge.go (скользящее окно и таймер OPEN), новый файл и новые слои не нужны. Wake cap остаётся отдельным сохраняемым счётчиком и не отменяется.
baseline: до правки `grep -c 'wakeCount\|wakeCap' internal/infra/cibridge/bridge.go` = 11, `grep -rn 'HALF_OPEN\|halfOpen\|circuitBreaker' internal/infra/cibridge | wc -l` = 0. После правки в bridge.go добавлены константы `breakerWindowSize = 10`, `breakerMinimumCalls = 3`, порог отказов 1/2 и `breakerOpenWait = 30s`; в bridge_test.go добавлены тесты `TestAdapterBreakerOpensAfterMinimumFailedCalls`, `TestAdapterBreakerHalfOpenProbeClosesBreaker`, `TestAdapterBreakerHalfOpenFailedProbeReopens`, `TestAdapterBreakerIgnoresDeadSessionFailures`, `TestAdapterBreakerKeepsWatchWakeCap`. Локально выполнен только `gofmt -w` на двух OwnerFiles; `go test` и `go build` не запускались по правилам репозитория, проверка идёт в CI PR.
Статус: done
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

## Подтверждение
Реализовано в точке `wakeOne` после проверки wake cap и до отправки wake. Состояние хранится в `Bridge.breakers`: ключ это имя адаптера, пустой адаптер попадает в ключ `legacy`. Слои не нарушены: использованы только stdlib `sync` и `time`, уже импортированные в bridge.go, новый файл не создан. Блокера нет.

Скользящее окно на десять результатов считает доли успехов и ошибок транспорта. Порог `minimumNumberOfCalls = 3` не даёт breaker открыться на одной ошибке. При доле отказов от 1/2 breaker уходит в OPEN на 30 секунд и skip-ит wake с причиной `adapter circuit breaker open`. После задержки одна проба уходит в HALF_OPEN: успех закрывает breaker, ошибка транспорта открывает заново, вторая проба в этом же HALF_OPEN не допускается.

Dead session через `isDeadSessionErr` идёт в `breakerIgnore`: он не попадает в окно и не открывает breaker, но по-прежнему помечает подписчика orphaned и уходит в ladder как indeterminate. Wake cap на Watch остался: тест `TestAdapterBreakerKeepsWatchWakeCap` проверяет, что предел в один wake паркует Watch даже при закрытом breaker.

Скользящее окно считает только статистику исходов и не ограничивает параллелизм. Для ограничения параллелизма нужен отдельный bulkhead, в этой карточке его нет.

gofmt применён к bridge.go и bridge_test.go. Локальные `go test` и `go build` не запускались по правилам AGENTS.md, тесты проверяет CI PR.

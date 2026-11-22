# WIRING-1608. Связать генерационные порты в composition root и тестах
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/mcp.go, cmd/beacon/mcp_test.go
OwnerFiles: см. поле «Владелец файлов»
Зависимости: PR 1608
Ссылка: PR 1608
SourceWatch: watch исходного PR #1608 закрыт после мержа, headSHA не зафиксирован в описании PR
PR: 1608
Агент: нет
Время: нет
Цель: Передать GenerationStore в beacon_task_claim и покрыть MP-01 тестами в cmd/beacon/mcp_test.go.
Контекст: PR #1608 добавил три порта ADR 0314 в mcp.Dependencies и применил их в beacon_stats и beacon_ack. Сторона claim осталась отложенной: beacon_task_claim не имеет сеттера генерации, а cmd/beacon/mcp.go был занят открытыми PR #1496 и #1601.
Шаги: Дождаться мержа PR 1608; передать GenerationStore из composition root в обработчик task claim в cmd/beacon/mcp.go; добавить покрытие в cmd/beacon/mcp_test.go; проверить, что beacon_stats по-прежнему получает Index.GraphStatus и статистику реестра; вызвать `beacon deferred done` с watchID из SourceWatch.
Проверка: При наличии GenerationLedger обработчик task claim получает store, тесты cmd/beacon зелёные в CI, запись WIRING-1608 переведена в done.
Приёмка: Проводка применена, запись в `beacon deferred --format json` имеет `status: done`, CI зелёный.

## Причина

cmd/beacon/mcp.go был занят открытыми PR #1496 и #1601, а beacon_claim не входит в объявленный набор инструментов. Привязку composition root и покрытие mcp_test.go записали отдельной карточкой, чтобы не расширять OwnerFiles PR #1608.

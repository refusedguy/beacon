# WIRING-1608. Связать генерационные порты в composition root и тестах
Статус: done
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/mcp.go, cmd/beacon/mcp_test.go
OwnerFiles: см. поле «Владелец файлов»
Зависимости: PR 1608
Ссылка: PR 1608
SourceWatch: watch исходного PR #1608 закрыт после мержа, headSHA не зафиксирован в описании PR
PR: 1608
Агент: ses_wiring1608_executor
Время: 2026-11-22T18:25:50Z
Цель: Передать GenerationStore в beacon_task_claim и покрыть MP-01 тестами в cmd/beacon/mcp_test.go.
Контекст: PR #1608 добавил три порта ADR 0314 в mcp.Dependencies и применил их в beacon_stats и beacon_ack. Сторона claim осталась отложенной: beacon_task_claim не имеет сеттера генерации, а cmd/beacon/mcp.go был занят открытыми PR #1496 и #1601.
Шаги: Дождаться мержа PR 1608; передать GenerationStore из composition root в обработчик task claim в cmd/beacon/mcp.go; добавить покрытие в cmd/beacon/mcp_test.go; проверить, что beacon_stats по-прежнему получает Index.GraphStatus и статистику реестра; вызвать `beacon deferred done` с watchID из SourceWatch.
Проверка: При наличии GenerationLedger обработчик task claim получает store, тесты cmd/beacon зелёные в CI, запись WIRING-1608 переведена в done.
Приёмка: Проводка применена, запись в `beacon deferred --format json` имеет `status: done`, CI зелёный.

Причина: Уже влито в origin/main, дубль кода не создаём. Источник PR #1608 мерджен (e31893b7153cea5e39b344823d858596ea3ba2ad). Серверная часть проводки влита PR #1660 (merge SHA 2d34645b54f45d84dba3a595350f17f8658759da, internal/infra/mcp/server.go): attachTaskClaim принимает Dependencies и связывает GraphIndexReader. Часть composition root и тесты влиты PR #1753 (merge SHA 355ba72a236c67d9ecce0504c6ea2f1c296bb3fa, ровно OwnerFiles cmd/beacon/mcp.go и cmd/beacon/mcp_test.go): buildMCPDependencies связывает GraphIndexReader, RegistryStatsReader и GenerationLedger, mcp_test.go проверяет все три порта. git merge-base --is-ancestor для обоих SHA дал exit 0. Открытого PR с этой же проводкой нет.
Закрыл: ses_wiring1608_executor
Время закрытия: 2026-11-22T18:27:28Z
## Причина

cmd/beacon/mcp.go был занят открытыми PR #1496 и #1601, а beacon_claim не входит в объявленный набор инструментов. Привязку composition root и покрытие mcp_test.go записали отдельной карточкой, чтобы не расширять OwnerFiles PR #1608.

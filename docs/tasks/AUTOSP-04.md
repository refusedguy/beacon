# AUTOSP-04. Slot Auto-Spawner в цикле reconciler
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/mcp.go, cmd/beacon/mcp_test.go
Зависимости: AUTOSP-05, AUTOSP-06, AUTOSP-09
Ссылка: ADR 0306
Агент: нет
Время: нет
Цель: Превратить терминальный сигнал волны в запуск следующего агента без сообщения владельца.
Контекст: Источник: docs/research/audit-01-autospawn.md, таблица строка 4 и цепочка пункты 5 и 6. internal/infra/cibridge/verdict_applier.go:472-477 формирует nextAction report_to_orchestrator. cmd/beacon/mcp.go:1954 ReconcileLoop и :1914 ReconcilePass не содержат spawn. cmd/beacon/daemon.go:500-529 DaemonPorts не имеет порта Spawn. baseline: rg на auto-spawn по cmd и internal дал 0 совпадений. Лимит 4 задан в internal/domain/orchestrator.go:17.
Шаги: Определить порт AutoSpawner и подключить его в reconcileConfig. На каждой итерации после bridge.Refresh выбрать свободный слот, получить следующую карточку через порт claim, прогнать гейты, создать worktree через существующий вызов после fetch, связать cardID и watchID, запустить сессию через spawnSessionSpawner или ACP-фабрику. Добавить флаг --auto-spawn в beacon up по месту флагов cmd/beacon/up.go:152 через владельца файла отдельной волной. Записывать инциденты в observe.
Проверка: CI зелёный. Тест mcp_test.go проверяет, что без свободного слота spawn не вызывается. Тест с терминальным вердиктом проверяет один вызов spawn. Тест с красным main не запускает агента.
Приёмка: Один сигнал report_to_orchestrator открывает ровно один слот и порождает ровно один запуск.

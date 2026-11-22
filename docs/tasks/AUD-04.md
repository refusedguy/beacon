# AUD-04. Перевести mcp.go на OpenProduction, лок close и подключить порты
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/mcp.go, cmd/beacon/mcp_test.go, cmd/beacon/registry_list.go, cmd/beacon/catalog.go
Зависимости: 
Ссылка: docs/research/BEACON-EXPLAINER-3.html, аудит fix/c18-a17
Агент: нет
Время: нет
Цель: Собрать в одном PR все правки cmd/beacon/mcp.go из аудита: OpenProduction, лок close, привязка cardID к watchID и подключение портов.
Контекст: Источник: аудит fix/c18-a17, P0.3, P0.4 и P1.7. openProcessStore зовёт store.Open вместо OpenProduction, из-за чего persist.seq всегда 0 и не работает sqlite-redact. closeLinkedCardFile и closeWiringCard правят .md без лока, а claimedCardForNotify ищет карточку через notify.Target и ломается при разных agentID. SetGraphIndexReader и SetRegistryStatsReader не вызываются, registryListSource не подключён, catalog.go не содержит 10 команд из runnerFor. baseline: persist.seq=0, 2 close без лока, 0 вызовов сеттеров, 10缺少 команд.
Шаги: 1) Переключить openProcessStore на store.OpenProduction и проверить совместимость запросов с sqlite-redact; добавить тест роста persist.seq и редактирования токенов. 2) Обернуть closeLinkedCardFile и closeWiringCard локом из AUD-05 и хранить привязку cardID к watchID явно, а не через notify.Target; добавить тест на разные agentID и notify.target. 3) Подключить GraphIndexReader, RegistryStatsReader, registryListSource и GenerationLedger в limits OwnerFiles; вернуть graphReady в TaskClaimOutput; синхронизировать catalog с runnerFor. Если порт требует чужие OwnerFiles, вызвать beacon_defer_wiring и завести отдельную карточку.
Проверка: CI зелёный. Тесты показывают рост persist.seq, редактирование токенов, закрытие под локом, поиск карточки при разных agentID и notify.target, данные индекса и реестра в beacon_stats и совпадение catalog с runnerFor.
Приёмка: Изменения только в OwnerFiles карточки. Уже существующие newReadOnlyOpencodePort, FileTaskClaimStore, FileTaskCloseStore, card_links и поддержку ATX-R01 не переделывать.

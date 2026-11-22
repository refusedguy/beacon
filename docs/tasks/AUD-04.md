# AUD-04. Перевести mcp.go на OpenProduction, лок close и подключить порты
Статус: done
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

Итог: Already landed in #1470, #1469, #1436, #1612, #1608 (d26bd3ba3856275a108d58863588e0f55705ff0e, 0efa2344ba84ced85d9dae11d63947613259818c, d0d9fc6fbf103972a8730de75f7b6116797c655f, add059c76e61582dc4d7110bff75cd2c0d9a27b0, e31893b7153cea5e39b344823d858596ea3ba2ad). No duplicate code needed.
Доказательство: PR #1470 commit d26bd3ba3856275a108d58863588e0f55705ff0e merge-base --is-ancestor d26bd3ba3856275a108d58863588e0f55705ff0e origin/main exit 0; PR #1469 commit 0efa2344ba84ced85d9dae11d63947613259818c merge-base --is-ancestor 0efa2344ba84ced85d9dae11d63947613259818c origin/main exit 0; PR #1436 commit d0d9fc6fbf103972a8730de75f7b6116797c655f merge-base --is-ancestor d0d9fc6fbf103972a8730de75f7b6116797c655f origin/main exit 0; PR #1612 commit add059c76e61582dc4d7110bff75cd2c0d9a27b0 merge-base --is-ancestor add059c76e61582dc4d7110bff75cd2c0d9a27b0 origin/main exit 0; PR #1608 commit e31893b7153cea5e39b344823d858596ea3ba2ad merge-base --is-ancestor e31893b7153cea5e39b344823d858596ea3ba2ad origin/main exit 0; no open duplicate PR.
Причина: already landed in #1470, #1469, #1436, #1612, #1608 d26bd3b, 0efa234, d0d9fc6, add059c, e31893b
Время закрытия: 2026-11-22T11:29:08Z

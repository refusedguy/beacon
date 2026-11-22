# AUD-09. Расширить remote-контракт до всех инструментов MCP
Статус: done
Premise: PR #1466, коммит 0482830cabc590e39877a1d655f4372a367ab4a5, добавил описания и фабрики входных схем для всех 10 инструментов в internal/infra/mcp/client.go. git merge-base --is-ancestor 0482830cabc590e39877a1d655f4372a367ab4a5 origin/main вернул 0. git show origin/main:internal/infra/mcp/client.go содержит 10 уникальных ключей beacon_* в remoteToolDescriptions.
baseline: было 7 из 10 в remoteToolDescriptions, стало 10 из 10. Проверка: git show origin/main:internal/infra/mcp/client.go | grep -o '"beacon_[a-z_]*"' | sort -u | wc -l даёт 10.
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/client.go, internal/infra/mcp/client_test.go
Зависимости: 
Ссылка: docs/research/BEACON-EXPLAINER-3.html, аудит fix/c18-a17
Агент: exe-mp04
Время: 2026-11-22T06:25:35Z
Цель: Описать и donner входные схемы для всех 10 инструментов MCP в удалённом клиенте.
Контекст: Источник: аудит fix/c18-a17, P1.6. internal/infra/mcp/server.go регистрирует 10 инструментов через toolDescriptors, но remoteToolDescriptions и remoteInputSchemaFactories в internal/infra/mcp/client.go содержат только 7, теряя beacon_defer_wiring, beacon_task_claim и beacon_task_close. baseline: 7 из 10.
Шаги: Дописать описания и фабрики входных схем для трёх缺少 инструментов. Сверить имена и схемы с toolDescriptors. Обновить контрактные тесты так, чтобы они падали при рассинхроне списка.
Проверка: CI зелёный. Тест проверяет равенство множеств имён в server и client и наличие входных схем у всех 10 инструментов.
Приёмка: Изменения только в OwnerFiles карточки. Уже существующие newReadOnlyOpencodePort, FileTaskClaimStore, FileTaskCloseStore, card_links и поддержку ATX-R01 не переделывать.

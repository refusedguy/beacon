# INTG-02. Сделать реестр генераций долговечным и совместимым с портами
Статус: done
Premise: PR #1456, коммит 7de845ba304dcd83ff7997603c69f04eafcd282a, добавил долговечный ledger с портом GenerationStore и восстановлением при создании в internal/app/orchestrator/claim.go. git merge-base --is-ancestor 7de845ba304dcd83ff7997603c69f04eafcd282a origin/main вернул 0. git show origin/main:internal/app/orchestrator/claim.go содержит ClaimRow (:38), GenerationStore (:63) и NewClaimGenerationWithStore (:119), который читает состояние из store.
baseline: было 0 долговечных реализаций, стала 1 с восстановлением генераций после рестарта. Проверка: git grep -n 'NewClaimGenerationWithStore' origin/main -- internal/app/orchestrator/claim.go даёт :115 и :119.
Фаза: P1
Тип: task
Владелец файлов: internal/app/orchestrator/claim.go, internal/app/orchestrator/claim_test.go
Зависимости: 
Ссылка: 
Агент: exe-mp04
Время: 2026-11-22T06:25:35Z
Цель: Обеспечить реализацию GenerationLedger и GenerationControl, которая переживает рестарт процесса.
Контекст: Источник: docs/research/audit-05-integrity.md, раздел "Новое, что не вошло в AUD", пункт 2. internal/infra/mcp/tool_claim.go:397 и tool_ack.go:472 определяют SetGenerationStore, internal/infra/cibridge/bridge.go:382 определяет WithGenerationStore. Вызовов вне тестов нет. Единственная реализация ClaimGeneration в internal/app/orchestrator/claim.go:79 хранит owners и rows в структурах процесса, поэтому после рестарта генерации сбрасываются. AUD-04 упоминает GenerationLedger, но не описывает ноль вызовов и потерю при рестарте. baseline: 0 calls of the setters, 0 durable implementations.
Шаги: Добавить в internal/app/orchestrator/claim.go конструктор, который принимает порт сохранения и восстанавливает генерации при создании. Сохранить сигнатуры IssueFor, GenerationFor и ResolveBlocked, чтобы тип по-прежнему удовлетворял GenerationLedger и GenerationControl без импорта infra-пакетов mcp и cibridge. Добавить тест в claim_test.go на поворот генерации после повторного открытия хранилища и на отсутствие повторного поворота. Вызов сеттеров в cmd/beacon/mcp.go оставить владельцу AUD-04 или оформить через beacon_defer_wiring.
Проверка: CI зелёный. Тест подтверждает, что генерация, выданная до пересоздания ledger, не принимается после него. Сигнатуры портов не изменились.
Приёмка: Изменения только в OwnerFiles карточки. ClaimGeneration сохраняет совместимость с mcp.GenerationLedger и cibridge.GenerationControl. Ни один infra-пакет не импортирует app.

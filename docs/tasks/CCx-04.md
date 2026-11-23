# CCx-04. Одобрение эскалированного повтора, а не первой попытки
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/app/orchestrator/orchestrator.go, internal/app/orchestrator/retry_approval_test.go
Зависимости: нет
Ссылка: docs/research/comp-codex.md, codex-rs/core/src/tools/orchestrator.rs:496-506, :521-532
Агент: ses_ccx04_executor
Время: 2026-11-22T17:05:58Z
Цель: Отдавать на одобрение те полномочия, с которыми реально выполнится повтор после отказа песочницы.
Контекст: Источник: docs/research/comp-codex.md. Codex переписывает действие повтора перед одобрением: ставит `SandboxPermissions::RequireEscalated` и убирает дополнительные разрешения (`codex-rs/core/src/tools/orchestrator.rs:521-532`). Он отдельно решает, можно ли пропустить одобрение повтора (`:496-506`). Beacon хранит одно булево поле `RequireApproval` в `AutoMergePolicy` (`internal/app/orchestrator/orchestrator.go:72-92`). Действия повтора и его полномочий в этой модели нет. Baseline: 1 флаг одобрения, 0 действий повтора, 0 полей эскалации.
Шаги: Выделить тип действия одобрения с полномочиями. Создавать отдельное действие для повтора после отказа. Помечать его эскалацией и пустым списком дополнительных разрешений. Покрыть тестом на различие первой попытки и повтора.
Проверка: CI зелёный. Тест показывает действие первой попытки с узкими разрешениями и действие повтора с 1 эскалацией и 0 дополнительных разрешений.
Приёмка: Изменения только в OwnerFiles карточки. Контракт MCP из 10 инструментов не расширять.

Причина: Already landed in PR #1731 (squash commit 935201f757d4bca3f357ec4aaa31baafb63e4a66). git merge-base --is-ancestor 935201f7 origin/main exit 0. No duplicate code PR opened.
Закрыл: ses_ccx04_executor
Время закрытия: 2026-11-22T17:06:20Z
## Подтверждение
- PR #2000 MERGED: 2026-11-22 (squash).


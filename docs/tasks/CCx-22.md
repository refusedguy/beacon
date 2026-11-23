# CCx-22. Namespace и маркеры шифрования в спецификации инструмента
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/mcp/schema.go, internal/infra/mcp/namespace_test.go
Зависимости: нет
Ссылка: docs/research/comp-codex.md, codex-rs/core/src/tools/multi_agent_tool.rs:28-46, :69-97
Агент: ses_ccx22b_executor
Время: 2026-11-22T18:23:45Z
Цель: Описывать инструменты multi-agent через namespace и сохранять пометки шифрования параметров.
Контекст: Источник: docs/research/comp-codex.md. Codex переносит в описание инструмента namespace и описание (`codex-rs/core/src/tools/multi_agent_tool.rs:69-97`). При подмене параметров он копирует пометку `encrypted: true` с исходной схемы (`:28-46`). Если пометка теряется, аргумент не доходит до спавненного агента. Beacon строит описания инструментов через `outputSchemaFactories` (`internal/infra/mcp/schema.go:476`). Контракт закрепляет ровно 10 плоских имён (`internal/infra/mcp/contract_test.go:40-42`). Namespace и шифрованных параметров в этой модели нет. Baseline: 0 namespace, 0 полей encrypted, 10 плоских имён инструментов.
Шаги: Добавить описание namespace в схему инструмента. Переносить пометки параметров при подмене схемы. Сохранить содержание инструментов без namespace для текущего контракта. Покрыть тестом на namespace и на потерю пометки encrypted.
Проверка: CI зелёный. Тест показывает 1 namespace с вложенным инструментом и 1 параметр с `encrypted: true` после подмены. Golden `tools/list` для 10 плоских имён остаётся без изменений.
Приёмка: Изменения только в OwnerFiles карточки. Контракт MCP из 10 инструментов и бюджет 20 KiB не расширять.

Причина: Already landed in origin/main via commit 87f60b6b (PR #1823) "feat(mcp): model tool namespace and encrypted parameter markers (CCx-22)". git merge-base --is-ancestor 87f60b6b origin/main exit 0. OwnerFiles schema.go/namespace_test.go already contain ToolNamespace, NamespacedTool, SetToolNamespace, ToolNamespaceOf, MarkParameterEncrypted, ParameterEncrypted and SubstituteToolSchema with acceptance test; later schema.go commits 42b96d35 and e0eb1743 are on top. No duplicate code.
Закрыл: ses_ccx22b_executor
Время закрытия: 2026-11-22T18:24:13Z
## Подтверждение
- PR #2139 MERGED: 2026-11-22 (squash).


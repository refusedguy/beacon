# CCx-06. Четыре режима политики песочницы
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/app/orchestrator/orchestrator.go, docs/evals/ccx06-sandbox-modes.md
Зависимости: нет
Ссылка: docs/research/comp-codex.md, codex-rs/protocol/src/protocol.rs:1293-1341
Агент: ses_ccx06b_executor
Время: 2026-11-22T17:19:20Z
Цель: Задавать restrictions выполнения явным режимом, а не набором разрозненных флагов.
Контекст: Источник: docs/research/comp-codex.md. Codex определяет `SandboxPolicy` с четырьмя вариантами: `danger-full-access`, `read-only`, `external-sandbox` и `workspace-write` (`codex-rs/protocol/src/protocol.rs:1293-1341`). Каждый вариант несёт свои флаги сети и записи. Beacon не имеет типа политики песочницы. `AutoMergePolicy` (`internal/app/orchestrator/orchestrator.go:72-92`) управляет только мержем, а `git clone --reference` (`cmd/beacon/agents.go:406-413`) даёт изоляцию репозитория без режима доступа. Baseline: 0 вариантов политики песочницы в типах beacon.
Шаги: Описать четыре режима в домене beacon. Разрешить выбор режима в конфиге запуска. Связать режим с проверкой записи и сети. Обновить eval с таблицей ожидаемого поведения.
Проверка: CI зелёный. Конфиг с каждым из 4 режимов загружается. Попытка записи в `read-only` даёт 1 отказ, а в `workspace-write` даёт 1 успех.
Приёмка: Изменения только в OwnerFiles карточки. Текущие имена флагов `attach --resume` и контракт MCP не меняются.

Причина: Already landed in PR #1833 (merge commit bf42922f47808f3bdbce4549c1f80d0150fa6bf7). git merge-base --is-ancestor bf42922f47808f3bdbce4549c1f80d0150fa6bf7 origin/main exit 0. SandboxPolicy with four modes, write/network checks and docs/evals/ccx06-sandbox-modes.md are present in origin/main. No duplicate code written.
Закрыл: ses_ccx06b_executor
Время закрытия: 2026-11-22T17:19:49Z
# CCx-07. Защита служебных путей внутри каталога с записью
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/app/orchestrator/orchestrator.go, internal/app/orchestrator/protected_paths_test.go
Зависимости: CCx-06
Ссылка: docs/research/comp-codex.md, codex-rs/protocol/src/protocol.rs:1343-1390
Агент: ses_ccx07b_executor
Время: 2026-11-22T17:34:17Z
Цель: Не давать агенту менять служебные каталоги даже внутри корня, куда разрешена запись.
Контекст: Источник: docs/research/comp-codex.md. Codex описывает `WritableRoot` со списками `read_only_subpaths` и `protected_metadata_names` (`codex-rs/protocol/src/protocol.rs:1343-1359`). Метод `is_path_writable` сначала проверяет корень, затем подпути только для чтения, затем имена служебных каталогов (`:1361-1390`). Список защищает `.codex`, `.git` и `.git/hooks` от повышения полномочий. Beacon не хранит такой список. Маркер worktree пишется в state dir без проверки пути (`internal/infra/worktree/marker.go:61-65`), а песочница создаётся через `git clone --reference` (`cmd/beacon/agents.go:406-413`). Baseline: 0 защищённых имён и 0 подпутей только для чтения.
Шаги: Добавить список защищённых имён и подпутей в режим песочницы. Проверить путь до записи файла. Отказывать в записи для первого компонента, который совпадает с защищённым именем. Покрыть тестом на `.git/hooks/x`, `.codex/config` и обычный путь.
Проверка: CI зелёный. Тест показывает 0 записанных файлов в `.git/hooks/x` и `.codex/config` и 1 записанный файл по пути `docs/tasks/CCx-07.md`.
Приёмка: Изменения только в OwnerFiles карточки. Правило never-overwrite для карточек сохранить.

Причина: Реализация уже в main. Коммит c4ec7e74ec5d5bbc5c814e263f0b1cd46a455b14, PR #1876. Проверка git merge-base --is-ancestor c4ec7e74 origin/main вернула 0. В main есть SandboxPolicy.ProtectedNames, protectedPathReason и тест internal/app/orchestrator/protected_paths_test.go. Дубль кода не нужен.
Закрыл: ses_ccx07b_executor
Время закрытия: 2026-11-22T17:34:41Z
## Подтверждение
- PR #2044 MERGED: 2026-11-22 (squash).


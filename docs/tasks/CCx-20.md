# CCx-20. Backlink административного каталога при перечислении worktree
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/worktree/list.go, internal/infra/worktree/backlink_test.go
Зависимости: нет
Ссылка: docs/research/comp-codex.md, codex-rs/worktree/src/lib.rs:230-252
Агент: ses_ccx20c_executor
Время: 2026-11-22T18:22:18Z
Цель: Не доверять устаревшей регистрации worktree без проверки обратной связи.
Контекст: Источник: docs/research/comp-codex.md. Codex при перечислении требует, чтобы admin-каталог чек-аута указывал обратно на этот же чек-аут (`codex-rs/worktree/src/lib.rs:230-252`). Он читает `gitdir`, канонализует путь и сравнивает его с `.git` кандидата. Несовпадение исключает запись из списка. Beacon разбирает вывод `git worktree list` в `parsePorcelain` (`internal/infra/worktree/gc.go:68-86`) и принимает каждую запись без backlink-проверки. Устаревший путь может удержать чужой чек-аут. Baseline: 0 backlink-проверок, 1 недоверенный парсер списка.
Шаги: Вынести проверку списка в отдельный файл. Для каждой записи читать `gitdir`, канонализовать его и сравнивать с `.git`. Исключать записи без совпадения. Покрыть тестом на подменённый чек-аут по старому пути.
Проверка: CI зелёный. Тест с подменённым чек-аутом показывает 0 записей в списке. Тест с корректным backlink показывает 1 запись.
Приёмка: Изменения только в OwnerFiles карточки. Формат `Entry` и причины из gc.go не менять.

Причина: Already landed in PR #1813, merge commit 0d433be935eda13eeceb5013c0a949472f3f0e25. git merge-base --is-ancestor against origin/main exits 0. The implementation is in internal/infra/worktree/list.go and internal/infra/worktree/backlink_test.go on origin/main, so no duplicate code was written.
Закрыл: ses_ccx20c_executor
Время закрытия: 2026-11-22T18:22:38Z
## Подтверждение
- PR #2131 MERGED: 2026-11-22 (squash).


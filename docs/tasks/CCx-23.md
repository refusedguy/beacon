# CCx-23. Политика symlink при записи patch
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/batchapply/apply.go, internal/infra/batchapply/symlink_test.go
Зависимости:
Ссылка: docs/research/comp-codex.md, codex-rs/core/src/tools/runtimes/apply_patch.rs:198-209
Агент: ccx23
Время: 2026-11-22T15:44:19Z
Цель: Отвергать символические ссылки только тогда, когда требуемая песочница была обойдена.
Контекст: Источник: docs/research/comp-codex.md. Codex передаёт в `apply_patch_with_options` флаг `follow_symlinks` (`codex-rs/core/src/tools/runtimes/apply_patch.rs:198-209`). Флаг true только при запрошенной песочнице или когда менеджер решил, что песочница нужна. Комментарий прямо говорит, что ссылки отвергаются при обходе обязательной песочницы. Beacon пишет файлы через `os.Stat` и `os.WriteFile` (`internal/infra/batchapply/apply.go:595-611`). Ссылки разыменовываются безусловно, sandbox context в вызове нет. Baseline: 0 проверок symlink, 1 неограниченная запись на `apply.go:611`.
Шаги: Передавать в writer признак того, что песочница требовалась. Проверять путь через `Lstat` до записи. Отвергать ссылку, ведущую вне корня, когда песочница обязательна. Покрыть тестом на символическую ссылку с обязательной и необязательной песочницей.
Проверка: CI зелёный. Тест с обязательной песочницей показывает 1 отказ и 0 записанных байтов по пути ссылки. Тест без обязательной песочницы записывает 1 файл.
Приёмка: Изменения только в OwnerFiles карточки. Правило never-overwrite для новых карточек сохранить.

Причина: блокировка снята. PR #1837 влит 2026-11-22T15:45:58Z squash-коммитом 95fac608afe9b25534bc2a0dbbb77383df768f07, `git merge-base --is-ancestor` даёт 0. Head 5348cae87c555a745fc93ddbcf6593635a09b9a3 не предок main, потому что squash-мерж не переносит исходный head. Зависимость CCx-06 закрыта PR #1833, коммит bf42922f47808f3bdbce4549c1f80d0150fa6bf7. Требование CCx-23 в main ещё не реализовано, поэтому карточка возвращена в todo.

## Подтверждение
- PR #1837 MERGED: https://github.com/refusedguy/beacon/pull/1837, squash-коммит 95fac608afe9b25534bc2a0dbbb77383df768f07, mergedAt 2026-11-22T15:45:58Z; head 5348cae87c555a745fc93ddbcf6593635a09b9a3 не предок main.
- `git show origin/main:internal/infra/batchapply/apply.go` не содержит Lstat, sandbox или символьных проверок; `git cat-file -e origin/main:internal/infra/batchapply/symlink_test.go` даёт код выхода 128.

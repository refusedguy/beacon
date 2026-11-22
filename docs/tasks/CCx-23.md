# CCx-23. Политика symlink при записи patch
Статус: blocked
Фаза: P1
Тип: task
Владелец файлов: internal/infra/batchapply/apply.go, internal/infra/batchapply/symlink_test.go
Зависимости: CCx-06
Ссылка: docs/research/comp-codex.md, codex-rs/core/src/tools/runtimes/apply_patch.rs:198-209
Агент: ccx23
Время: 2026-11-22T15:44:19Z
Цель: Отвергать символические ссылки только тогда, когда требуемая песочница была обойдена.
Контекст: Источник: docs/research/comp-codex.md. Codex передаёт в `apply_patch_with_options` флаг `follow_symlinks` (`codex-rs/core/src/tools/runtimes/apply_patch.rs:198-209`). Флаг true только при запрошенной песочнице или когда менеджер решил, что песочница нужна. Комментарий прямо говорит, что ссылки отвергаются при обходе обязательной песочницы. Beacon пишет файлы через `os.Stat` и `os.WriteFile` (`internal/infra/batchapply/apply.go:595-611`). Ссылки разыменовываются безусловно, sandbox context в вызове нет. Baseline: 0 проверок symlink, 1 неограниченная запись на `apply.go:611`.
Шаги: Передавать в writer признак того, что песочница требовалась. Проверять путь через `Lstat` до записи. Отвергать ссылку, ведущую вне корня, когда песочница обязательна. Покрыть тестом на символическую ссылку с обязательной и необязательной песочницей.
Проверка: CI зелёный. Тест с обязательной песочницей показывает 1 отказ и 0 записанных байтов по пути ссылки. Тест без обязательной песочницы записывает 1 файл.
Приёмка: Изменения только в OwnerFiles карточки. Правило never-overwrite для новых карточек сохранить.

Причина: Проверка premise перед кодом нашла открытый PR #1837 (ветка fix/cbx08, head 5348cae87c555a745fc93ddbcf6593635a09b9a3), который меняет OwnerFile internal/infra/batchapply/apply.go. По правилу контракта исполнителя код не пишется, пока по OwnerFile есть открытый PR. Требование CCx-23 в origin/main отсутствует: `git log --all --grep=CCx-23 -i` не дал коммитов, в apply.go нет проверок Lstat и sandbox, symlink_test.go не существует, PR с CCx-23 нет ни открытого, ни влитого. PR #1837 это CBX-08 (машиночитаемый отчёт импорта), а не дубль CCx-23; нужно решение владельца, продолжать ли после влития #1837.
Закрыл: ccx23
Время закрытия: 2026-11-22T15:44:19Z

## Подтверждение
- PR #1837 OPEN: https://github.com/refusedguy/beacon/pull/1837, head 5348cae87c555a745fc93ddbcf6593635a09b9a3, меняет internal/infra/batchapply/apply.go и apply_test.go.
- `git show origin/main:internal/infra/batchapply/apply.go` не содержит Lstat, sandbox или символьных проверок; `git cat-file -e origin/main:internal/infra/batchapply/symlink_test.go` даёт код выхода 128.

# CCx-19. Удаление worktree при игнорируемых локальных файлах
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/worktree/gc.go, internal/infra/worktree/remove_guard_test.go
Зависимости: нет
Ссылка: docs/research/comp-codex.md, codex-rs/worktree/src/lib.rs:284-317
Агент: нет
Время: нет
Цель: Не удалять worktree, в котором есть локальные файлы, включая игнорируемые.
Контекст: Источник: docs/research/comp-codex.md. Codex перед удалением проверяет, что текущий каталог не лежит внутри worktree (`codex-rs/worktree/src/lib.rs:301-303`). Затем он вызывает `git ls-files --others --ignored --exclude-standard -z` и отказывается удалять, если есть игнорируемые файлы (`:304-317`). Beacon проверяет только `git status --porcelain` (`internal/infra/worktree/gc.go:89-103`). Эта команда не показывает игнорируемые файлы. `Remove` (`gc.go:108-119`) не делает дополнительной проверки. Baseline: 1 проверка porcelain, 0 проверок игнорируемых файлов.
Шаги: Добавить проверку `ls-files --others --ignored --exclude-standard` перед `Remove`. Отдельно запретить удаление изнутри целевого worktree. Добавить новую причину удержания в список reasons. Покрыть тестом с игнорируемым файлом внутри worktree.
Проверка: CI зелёный. Тест с игнорируемым файлом показывает 1 удержание и 0 вызовов `git worktree remove`. Чистый worktree удаляется 1 раз.
Приёмка: Изменения только в OwnerFiles карточки. Причины `ReasonDirty` и `ReasonLocked` сохранить.

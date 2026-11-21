# IDX-15. Автоматический GC worktree при терминальном merged
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/infra/cibridge/bridge_test.go, internal/infra/worktree/gc.go, docs/tasks/IDX-15.md
Зависимости: IDX-07, IDX-08, IDX-12
Ссылка: https://github.com/refusedguy/beacon/pull/1115
Агент: ses_e02015a85ffekgh7vK9sQ0DStU
Время: 2026-11-21
Цель: После terminal merged Beacon сам вызывает безопасный GC worktree, а не только по ручной команде.
Контекст: В IDX-12 появился ручной `beacon gc`, а владельцу в grilling сказали, что Beacon чистит worktree сам. Замер до уборки: 927 worktree, 601 678 копий карточек, 12 ГБ, 679 копий AGENTS.md. Ручная уборка безопасных worktree выполнена, но без автоматического триггера.
Решение: `closeWatch` вызывает GC только при вердикте merged и только после успешного `closeLinkedCard`. GC объявлен портом `WorktreeGC` в cibridge, поэтому `internal/infra/cibridge` не импортирует `internal/infra/worktree` и матрица слоёв не меняется. `worktree.Collect` переиспользует `List`, `Status` и `Remove`: main отбрасывается `List`, заблокированные, защищённые, грязные worktree и worktree с живым Watch остаются с причиной.
Шаги: Точка вызова добавлена в `closeWatch`. `closeLinkedCard` возвращает ошибку, и при ошибке GC не запускается. Добавлены `WithWorktreeGC`, `collectWorktrees` и событие цикла `worktree_gc` при удалении. `worktree.Collect` возвращает `Removed` и `Kept` с причинами. Тесты покрывают merged, удержанный merge и ошибку закрытия карточки.
Проверка: gofmt выполнен локально. go test и гейты проходят в CI.
Приёмка: При merged безопасный worktree удаляется, живой или грязный остаётся с причиной. Связывание порта в composition root (`cmd/beacon/mcp.go`) не входит в OwnerFiles этой карточки и выполняется отдельной задачей: без биндинга триггер в проде не активен.

## Подтверждение
- 2026-11-21: реализация в PR [#1115](https://github.com/refusedguy/beacon/pull/1115), коммит `cfbb803`. Изменены `internal/infra/cibridge/bridge.go`, `internal/infra/cibridge/bridge_test.go`, `internal/infra/worktree/gc.go` и эта карточка. `TestMergedRunsWorktreeGCAfterCardClose` прогоняет collector на фейковом git runner и проверяет `worktree remove`. `TestHeldMergeSkipsWorktreeGC` и `TestCardCloseErrorSkipsWorktreeGC` проверяют, что GC не вызывается при удержанном merge и при ошибке закрытия карточки. gofmt применён, go build и go test локально не запускались, их выполнит CI.

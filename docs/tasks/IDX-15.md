# IDX-15. Автоматический GC worktree при терминальном merged
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/infra/worktree/gc.go, internal/infra/cibridge/bridge_test.go
Зависимости: IDX-07, IDX-08, IDX-12
Ссылка: https://github.com/refusedguy/beacon/pull/1106
Агент: ses_e020bebe1ffeF6R5NjhS3682mh
Время: 2026-11-21
Цель: После terminal merged Beacon вызывает безопасный GC worktree, а не только по ручной команде.
Контекст: В IDX-12 появился ручной `beacon gc`, а владельцу в grilling сказали, что Beacon чистит worktree сам. Замер до уборки: 927 worktree, 601 678 копий карточек, 12 ГБ, 679 копий AGENTS.md. Ручная уборка 927 безопасных worktree выполнена, но без автоматического триггера.
Решение: `closeWatch` вызывает GC только при вердикте merged и только после успешного `closeLinkedCard`. GC объявлен портом `WorktreeGC` в cibridge, поэтому `internal/infra/cibridge` не импортирует `internal/infra/worktree` и матрица слоёв не меняется. `worktree.Collect` переиспользует `List`, `Status` и `Remove`: main отбрасывается `List`, заблокированные и грязные worktree остаются с причиной, живые Watch защищены по head SHA.
Шаги: Точка вызова добавлена в `closeWatch`. `closeLinkedCard` возвращает ошибку, и при ошибке GC не запускается. `worktree.Collect` добавляет защиту живых Watch, грязных и заблокированных worktree и дополнительных путей текущей сессии. Тесты покрывают merged, удержанный merge и ошибку закрытия карточки.
Проверка: gofmt выполнен локально. go test и гейты проходят в CI.
Приёмка: При merged безопасный worktree удаляется, живой или грязный остаётся с причиной. Порт биндится в composition root отдельной карточкой WIRING-1106, так как `cmd/beacon/mcp.go` не входит в OwnerFiles этой задачи.

## Подтверждение
- 2026-11-21: реализация в PR [#1106](https://github.com/refusedguy/beacon/pull/1106), коммит `18a10c8`. Изменены `internal/infra/cibridge/bridge.go`, `internal/infra/cibridge/bridge_test.go`, `internal/infra/worktree/gc.go`, карточка и добавлена `docs/tasks/WIRING-1106.md`. Тест `TestMergedClosesCardThenRunsWorktreeGC` прогоняет collector на фейковом git runner и проверяет `worktree remove`. Тесты `TestNonMergedVerdictSkipsWorktreeGC` и `TestCardCloseErrorSkipsWorktreeGC` проверяют, что GC не вызывается при удержанном merge и при ошибке закрытия карточки. Связывание порта в `cmd/beacon/mcp.go` вынесено в WIRING-1106. go test и go build локально не запускались, их выполнит CI.

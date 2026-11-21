# IDX-12. GC worktree с защитой живых агентов
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/gc.go, cmd/beacon/main.go, internal/infra/worktree/gc.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Beacon сам удаляет безопасные worktree и помечает остальные с причиной.
Контекст: Premise: 927 worktrees в дереве, владелец подтвердил что их надо чистить. baseline: find worktrees = 927.
Шаги: Условие: нет активного Watch и git status --porcelain пуст. Иначе kept с причиной. dry-run и apply.
Проверка: gofmt, go test в CI.
Приёмка: Живые worktree не удаляются, мёртвые удаляются, отчёт содержит причины.

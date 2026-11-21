# IDX-15. Автоматический GC worktree при терминальном merged
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/infra/worktree/gc.go, internal/infra/cibridge/bridge_test.go
Зависимости: IDX-07, IDX-08, IDX-12
Ссылка: 
Агент: нет
Время: нет
Цель: После terminal merged Beacon сам вызывает безопасный GC worktree, а не только по ручной команде.
Контекст: Premise: в IDX-12 заложен ручной beacon gc, а в grilling владельцу было сказано что Beacon чистит сам. Расхождение зафиксировано. Исходный замер: 927 worktree, 601 678 копий карточек, 12 ГБ, только 679 копий AGENTS.md. Ручная уборка 927 безопасных worktree выполнена отдельно, но без автоматического триггера.
Шаги: В точке терминального merged и закрытия карточки вызывать GC с теми же условиями: нет активного Watch и git status --porcelain пуст. Живые и грязные worktree помечаются kept с причиной. Не удалять worktree текущей сессии и main. Добавить метрику удалённых и kept, тест на merged, на живой Watch и на грязный worktree.
Проверка: gofmt, go test и гейты в CI.
Приёмка: После merged безопасный worktree удаляется автоматически, живой или грязный остаётся с причиной, событие видно в метриках.

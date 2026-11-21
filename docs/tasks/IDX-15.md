# IDX-15. Автоматический GC worktree при терминальном merged
Блокер: точный блокер с именем файла `internal/infra/cibridge/bridge.go`. Автоматического GC при терминальном merged в main нет: PR #906 только добавил саму карточку, PR #911 (IDX-12) дал ручной `beacon gc` с защитой живых Watch, но триггера на merged нет. Точка входа для триггера это `applyMerged` (строка 1072) и `closeWatch` (строка 1124) в `internal/infra/cibridge/bridge.go`, где уже вызывается `closeLinkedCard` (строка 1140). Рядом нужно вызывать безопасный GC. Тест на merged, живой Watch и грязный worktree требует правки `internal/infra/cibridge/bridge_test.go`, метрика удалённых и kept требует правки `internal/infra/observe/metrics.go`. Статус todo сохранён.
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

## Подтверждение
- Premise проверен 2026-11-21 по свежему origin/main (4943794). `git log origin/main -- docs/tasks/IDX-15.md` даёт только e4c2fa9 (#906, добавление карточки); `gh pr list --state all --search 'GC worktree OR auto GC OR IDX-15'` даёт #906 (docs), #911 (ручной GC IDX-12), #951 (wiring) и #899 (docs). Ни один из них не добавляет автотриггер. В `internal/infra/cibridge/bridge.go` на main нет вызова GC в `applyMerged` и `closeWatch`; `internal/infra/worktree/gc.go` содержит только `List`, `Status` и `Remove` для ручной команды. Карточка остаётся todo с точным блокером `internal/infra/cibridge/bridge.go`. Код не писал, gofmt не применял, чужие файлы, wiring и .beacon не трогал, правка только текстовая.
- Перепроверка 2026-11-21 по свежему origin/main (4943794). Все коммиты c716b61..4943794 относятся к docs(tasks), код не менялся. `git log c716b61..origin/main -- internal/infra/cibridge/bridge.go internal/infra/worktree/gc.go internal/infra/cibridge/bridge_test.go internal/infra/observe/metrics.go` пуст. Повторный `gh pr list --state all --search 'GC worktree OR auto GC OR IDX-15'` не показывает PR с автотриггером. Блокер на месте: `git show origin/main:internal/infra/cibridge/bridge.go` даёт `applyMerged` на строке 1072, `closeWatch` на строке 1124 и вызов `closeLinkedCard` на строке 1140, без вызова GC. Строка `closeWatch` в тексте карточки исправлена с 1122 на 1124. `internal/infra/worktree/gc.go` по-прежнему содержит только `List`, `Status`, `Remove` и вспомогательные функции. Статус todo сохранён.

# Истощение бюджета ребейзов: разбор PR #1868

Справочная заметка. Фиксирует повторяющийся блокер `rebase_budget_exhausted` и решение, которое принимает владелец. Код PR здесь не меняется.

## Причина

Watch `watch:refusedguy/beacon:1868:...` находится в ревизии 11 с вердиктом `blocked`. Поля блокировки: `reason=rebase_budget_exhausted`, `blockedBy=cibridge`, `failedChecks=["rebase-budget-exhausted"]`. Бюджет `maxRebases=3` исчерпан, состояние мержа `CONFLICTING/DIRTY`.

Пять проверок падают одной и той же ошибкой компиляции из устаревшей базы PR:

- `internal/app/session/worktree_slug.go:27`: `Worktree redeclared`, а в строке 75 `wt.Name undefined`.
- `internal/app/session/branch_candidates.go:16`: `other declaration`.

Текущий `origin/main` переименовал тип в `SlugWorktree`. В `worktree_slug.go:27` теперь объявлен `SlugWorktree`, а `Worktree` остался в `branch_candidates.go:16`. Старая ветка PR объявляет `Worktree` в `worktree_slug.go`, поэтому компиляция падает до всяких изменений в самом PR. Команда `git merge-tree` дополнительно показывает конфликт содержимого в `internal/app/taskcli/service_test.go`.

cibridge списывает каждый ребейз в постоянное обязательство с ключом `(watchID, revision, rebase-counter)` и считает их через все ревизии Watch. Значение по умолчанию равно 3 (ADR 0259), диапазон 1..10. Когда число попыток достигает бюджета, следующий вердикт `rebase` вызывает `blockRebaseExhausted`. Переход переводит Watch в `blocked` и присоединяет его к общему `BlockerIncident` на базовой ветке.

## Почему rerun не помогает

Повтор проверок не меняет ни базу PR, ни счётчик. Счётчики хранятся в store и переживают перезапуск процесса.

- `spendRebase` отклоняет вторую попытку на той же паре `(watchID, revision)`. Обязательство уже записано, новый ребейз не выдаётся.
- Цикл пропускает Watch в статусе `blocked`. Команда `retry` повторяет те же пять падений компиляции и снова запрашивает ребейз, которого бюджет не разрешает.
- Пока база PR устарела, вердикт остаётся `rebase`. Повторные прогоны добавляют только шум в CI.

## Что нужно от владельца: поднять бюджет или снять блокировку

Владелец выбирает один из двух путей. Сам по себе выбор не убирает конфликт в `service_test.go`.

1. Поднять бюджет. Значение задаётся в `policy.maxRebases` при регистрации Watch через `beacon_watch_pr` или в `.beacon/config.yaml` для новых Watch. Диапазон 1..10, по умолчанию 3. Повышение даёт место для ещё одной попытки ребейза, но не разрешает конфликт.
2. Снять блокировку через `beacon_ack decision=blocked:resolved` или закрыть общий `BlockerIncident`. Это убирает статус `blocked`, но не возвращает уже потраченные попытки. После снятия Watch снова увидит вердикт `rebase` и упрётся в тот же бюджет. Чтобы сдвинуться, нужен либо первый путь, либо новая головная ревизия PR после ручного ребейза.

Рекомендация: сначала разрешить конфликт и запушить новый head SHA, затем поднять бюджет для повторной регистрации Watch. Так бюджет тратится на ребейз с уже исправленной базой.

## После снятия: ребейз с разрешением конфликта в service_test.go

1. Выполнить `git fetch origin main` и ребейз ветки PR на свежий `origin/main`.
2. Разрешить конфликт содержимого в `internal/app/taskcli/service_test.go`, оставив версию, совместимую с текущими вызовами.
3. Сверить `internal/app/session`: `worktree_slug.go` объявляет `SlugWorktree`, `branch_candidates.go` объявляет `Worktree`. Дублирование типов и ошибка `wt.Name undefined` должны уйти.
4. Запушить новую ветку и зарегистрировать Watch на тот же PR с новым `headSHA`. Смена головы закрывает прежний Watch по `CloseReasonHeadChanged`.
5. Дальше проверки прогоняет CI на PR. Локальные `go test`, `go build` и `beacon gates` не запускать.

## Как отличить от внешнего stale-lock провала

Сначала прочитать `beacon_get_result`. Блокер из этой заметки распознаётся по трём полям одновременно: `reason=rebase_budget_exhausted`, `blockedBy=cibridge`, `failedChecks=["rebase-budget-exhausted"]`. Это состояние Watch, а не имя проверки CI.

Внешний stale-lock выглядит иначе:

- Оставленный `git worktree lock` виден в отчёте `beacon gc-worktree` с причиной `locked`. Он мешает удалению каталога, но не переводит Watch в `blocked` и не тратит бюджет. Лечение: `git worktree unlock <путь>` (runbook `worktree-gc.md`).
- Ошибка захвата блокировки в хранилище или CI даёт собственный текст в `failedChecks` и не несёт причину `rebase_budget_exhausted`. Такой сигнал уходит FIX-агенту по правилу блокера main, а не владельцу на решение о бюджете.
- Падения из устаревшей базы повторяются на том же наборе файлов `internal/app/session/*` при любом числе rerun. Ошибка worktree lock указывает на путь каталога, а не на строки кода.

Если в `beacon_get_result` нет причины `rebase_budget_exhausted`, это другой блокер. Проверять счётчик `rebaseUsed` и `rebaseBudget` имеет смысл только после совпадения всех трёх полей.

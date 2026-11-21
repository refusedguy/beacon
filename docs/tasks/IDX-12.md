# IDX-12. GC worktree с защитой живых агентов
Статус: done
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/gc.go, cmd/beacon/main.go, internal/infra/worktree/gc.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Beacon сам удаляет безопасные worktree и помечает остальные с причиной.
Контекст: Premise: 927 worktrees в дереве, владелец подтвердил что их надо чистить. baseline: find worktrees = 927.
baseline: 927 worktrees в дереве; 2 активных Watch по данным beacon_list_running от 2026-11-21 (PR 886 и PR 902).
Шаги: Условие: нет активного Watch и git status --porcelain пуст. Иначе kept с причиной. dry-run и apply.
Проверка: gofmt, go test в CI.
Приёмка: Живые worktree не удаляются, мёртвые удаляются, отчёт содержит причины.

## Подтверждение
- Premise проверен 2026-11-21: `git worktree list --porcelain` показывает 927 worktrees, `beacon_list_running` показывает 2 активных Watch. Решение защищать worktree по совпадению HEAD с headSHA живого Watch выбрано после этого замера.
- Добавлен `internal/infra/worktree/gc.go` со списком, проверкой `git status --porcelain` и удалением через `git worktree remove` без `--force`.
- Добавлена команда `beacon gc-worktree` с просмотром по умолчанию и флагом `--apply`. Живой Watch, заблокированный worktree, грязный worktree и ошибка status дают `kept` с причиной в отчёте table или json.
- Зарегистрированы `gc` и `gc-worktree` в dispatch `cmd/beacon/main.go`. Код Q-A-02 в `cmd/beacon/gc.go` не изменён, добавлен только новый блок.
- Локально выполнен `gofmt -w` на свои .go. `go test` и `go build` прогоняет CI на PR. Статус карточки не менялся вручную, его обновит Beacon при `merged`.
- Проверено 2026-11-21 по `git log` и `gh pr view`. Реализация влита в origin/main через PR #911, https://github.com/refusedguy/beacon/pull/911. Merge commit e84c56ab202f13f6f636d19eea9059ff66266fcb. Статус переведён в done.

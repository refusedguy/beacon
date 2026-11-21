# WIRING-887. Подключить beacon gc к dispatch CLI
Premise: проверено 2026-11-21 по свежему origin/main (cd50c15). PR #951 влит, commit 6e4b918 лежит в origin/main. Dispatch сохранил case "gc" и "gc-worktree", каталог получил записи gc и gc-worktree, тест каталога обновлён. Матрица слоёв не менялась. Дубля нет. Правка только текстовая.
Статус: done
baseline: `git rev-parse --short origin/main` = cd50c15; `gh pr view 951 --repo refusedguy/beacon --json state,mergedAt` = MERGED 2026-11-21T10:19:57Z; `git merge-base --is-ancestor 6e4b918 origin/main` = 0; `git grep -n -E '"(gc|gc-worktree)"' origin/main -- cmd/beacon/catalog.go cmd/beacon/main.go` = catalog.go:28,29, main.go:286,288.
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/main.go, cmd/beacon/commands.go, cmd/beacon/catalog.go
OwnerFiles: cmd/beacon/main.go, cmd/beacon/commands.go, cmd/beacon/catalog.go
Зависимости: PR 887
Ссылка: PR 887
SourceWatch: watch:refusedguy/beacon:887:998c27c4b278d02cd4925c2c68c2c1ba6a3727ae
PR: 887
Агент: нет
Время: нет
Цель: beacon gc и beacon gc-worktree вызываются из dispatch через зарегистрированные runner-ы и присутствуют в каталоге команд.
Контекст: deferred-карточка PR 887. Причина отложенной проводки: dispatch beacon gc живёт в cmd/beacon/main.go вне OwnerFiles Q-A-02. RegisterGC и RegisterGCWorktree определены в gc.go и устанавливаются в init() этого файла; dispatch уже вызывает gcRunner и gcWorktreeRunner. Этот PR добавляет записи команд в каталог и фиксирует проводку карточкой.
Шаги: сохранить case "gc" и case "gc-worktree" в dispatch, сохранить вызовы RegisterGC и RegisterGCWorktree в composition root, добавить записи gc и gc-worktree в catalog и ожидаемые имена в тест каталога.
Проверка: `beacon list --format json` содержит gc и gc-worktree, `beacon gc --format table` и `beacon gc-worktree --format table` приходят в dispatch, gofmt без замечаний.
Приёмка: dispatch-ветки и регистрации на месте, каталог и тест обновлены, карточка и отложенная запись переведены в done после влития PR.

## Причина

Команды gc и gc-worktree реализованы в gc.go, но cmd/beacon/main.go и catalog.go не входили в OwnerFiles исходной карточки Q-A-02. Проводкаispatch и каталога вынесена в отдельную wiring-карточку.

## Шаги

1. Подтвердить, что dispatch вызывает gcRunner и gcWorktreeRunner, а gc.go регистрирует их через RegisterGC и RegisterGCWorktree.
2. Добавить записи gc и gc-worktree в cmd/beacon/catalog.go.
3. Обновить ожидаемый список имён в cmd/beacon/main_test.go.
4. Сохранить матрицу слоёв без изменений.

## Проверка

- `beacon gc` и `beacon gc-worktree` доступны из CLI.
- Каталог команд и тест каталога согласованы.

## Приёмка

Проводка применена, CI зелёный, карточка и отложенная запись переведены в done.

## Подтверждение
- Premise проверен 2026-11-21 по origin/main (cd50c15). PR #951 влит 2026-11-21, commit 6e4b918 лежит в origin/main. Dispatch сохранил case "gc" и "gc-worktree", cmd/beacon/catalog.go содержит записи gc и gc-worktree, cmd/beacon/main_test.go обновил ожидаемые имена. RegisterGC и RegisterGCWorktree установлены в gc.go через init(). Ребро в layers.json не добавлялось. Код не писал, чужие OwnerFiles и .beacon/ не трогал, правка только текстовая. Статус изменён с todo на done. Ссылка: https://github.com/refusedguy/beacon/pull/951.

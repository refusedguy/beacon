# WIRING-887. Подключить beacon gc к dispatch CLI
Статус: todo
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

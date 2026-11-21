# WIRING-919. Подключить beacon gc и beacon init к dispatch CLI
Статус: done
Фаза: P3
Тип: task
Владелец файлов: cmd/beacon/main.go, cmd/beacon/catalog.go, cmd/beacon/main_test.go, cmd/beacon/catalog_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Реализованные RegisterGC и RegisterInit доступны как команды beacon gc, beacon gc-worktree и beacon init.
Контекст: Premise: Q-A-02 оставил RegisterGC без dispatch, Q-D-02 оставил RegisterInit без dispatch. TestT06ListJSONCatalog жёстко ожидал 18 команд, поэтому агенты не трогали catalog. После влития board и gc-worktree数与 каталога вырос._owner.
Шаги: Добавить case в dispatch для gc, gc-worktree и init, обновить catalog и фикстуры тестов, прогнать self-test, сохранить Register seams.
Проверка: gofmt, go test и каталог-тесты в CI.
Приёмка: beacon gc, beacon gc-worktree и beacon init вызываются из CLI, каталог-тест зелёный.

## Подтверждение

PR #951 (commit 6e4b918) влит в origin/main 2026-11-21. Он добавил case "init", вызов RegisterInit(runInit), записи gc, gc-worktree и init в каталог и обновил TestT06ListJSONCatalog до 23 команд. Ветки case "gc" и case "gc-worktree" появились раньше в PR #911 (commit e84c56a). Все команды из цели карточки есть в dispatch и каталоге origin/main. Заголовок PR #951 перечисляет WIRING-887/915/926/931 и не называет WIRING-919, поэтому Beacon не перевёл карточку автоматически. OwnerFiles не менял.

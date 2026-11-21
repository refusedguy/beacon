# WIRING-919. Подключить beacon gc и beacon init к dispatch CLI
Статус: todo
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

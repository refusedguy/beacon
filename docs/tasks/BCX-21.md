# BCX-21. Собрать каталог команд из dispatch и убрать мёртвую up
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/catalog.go, cmd/beacon/main.go, cmd/beacon/up.go
Зависимости: 
Ссылка: ADR 0016
Агент: нет
Время: нет
Цель: Сделать beacon list --format json полным каталогом работающих команд.
Контекст: Источник: docs/research/cli-ux-audit.md, таблица 5 строка 1. dispatch в main.go:229-293 содержит 27 подкоманд, catalog.go:20-36 плюс три init() дают 18 записей. В каталоге нет test, orchestrate, orchestrator, registry, deliver, ci, daemon, workload, metrics, pr; есть up, который dispatch не знает. beacon what metrics даёт код 2, beacon up даёт неизвестная подкоманда. Базовый замер и приоритет из отчёта: P0.
Шаги: Построить каталог из одного источника с dispatch. Подключить up к dispatch или удалить запись и команду. Добавить тест равенства множества каталога и dispatch, включая вложенные подкоманды.
Проверка: CI зелёный. Тест каталога проходит. beacon what для каждой команды из dispatch возвращает описание и код 0.
Приёмка: Каталог содержит ровно рабочие команды. Агент может вызвать любую команду, прочитанную из beacon list --format json.

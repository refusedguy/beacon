# BCX-23. Подключить registry list/check/sync и gc к dispatch
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/registry_stop.go, cmd/beacon/watch.go, cmd/beacon/main.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Сделать доступными команды, раннеры и флаги которых уже написаны.
Контекст: Источник: docs/research/cli-ux-audit.md, таблица 5 строка 3. runRegistry в registry_stop.go:214-228 знает только case stop; registryListRunner (registry_list.go:50), registryCheckRunner (registry_check.go:232), syncRegistry (registry_sync.go:174) существуют. gcRunner и runGC в watch.go:752-758, :821-872 не имеют case gc в dispatch. Команды дают неизвестная подкоманда, код 2. Базовый замер и приоритет из отчёта: P1.
Шаги: Подключить раннеры в runRegistry и dispatch. Сверить с T5.53, T5.55 и T5.61. Если команды признаны мёртвыми, удалить файлы и обновить карточки. Добавить тест вызова каждой подкоманды.
Проверка: CI зелёный. beacon registry list, check, sync и beacon gc запускаются и печатают результат. Тест dispatch покрывает все четыре.
Приёмка: Ни один готовый раннер не остаётся недостижимым из CLI.

# BCX-23. Подключить registry list/check/sync и gc к dispatch
Статус: done
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/registry_stop.go, cmd/beacon/watch.go, cmd/beacon/main.go
Зависимости: 
Ссылка: PR #1283
Агент: exe139
Время: 2026-11-21T18:06:42Z
Цель: Сделать доступными команды, раннеры и флаги которых уже написаны.
Контекст: Источник: docs/research/cli-ux-audit.md, таблица 5 строка 3. runRegistry в registry_stop.go:214-228 знает только case stop; registryListRunner (registry_list.go:50), registryCheckRunner (registry_check.go:232), syncRegistry (registry_sync.go:174) существуют. gcRunner и runGC в watch.go:752-758, :821-872 не имеют case gc в dispatch. Команды дают неизвестная подкоманда, код 2. Базовый замер и приоритет из отчёта: P1.
Шаги: Подключить раннеры в runRegistry и dispatch. Сверить с T5.53, T5.55 и T5.61. Если команды признаны мёртвыми, удалить файлы и обновить карточки. Добавить тест вызова каждой подкоманды.
Проверка: CI зелёный. beacon registry list, check, sync и beacon gc запускаются и печатают результат. Тест dispatch покрывает все четыре.
Приёмка: Ни один готовый раннер не остаётся недостижимым из CLI.
Итог: Уже влито в origin/main, PR #1283 (MERGED). Все четыре раннера достижимы из CLI: registry list и check через case в registryCommand, registry sync через case "sync" в runRegistry, gc через ключ "gc" в runnerFor. Дубль кода не нужен.
Доказательство: 9d83598a9491012fc513d812a7df2fe87151dad3 в origin/main, `git merge-base --is-ancestor 9d83598a9491012fc513d812a7df2fe87151dad3 origin/main` = exit 0.

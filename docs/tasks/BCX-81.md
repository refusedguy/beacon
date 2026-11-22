# BCX-81. Поставить автоматическую уборку сирот opencode serve
Статус: done
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/gc.go, cmd/beacon/main.go, internal/infra/opencode/server.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Не допускать накопления сирот после смерти beacon mcp.
Контекст: Источник: docs/research/process-leak-rootcause.md, RC-2 и RC-3, меры 3; process-leak-mechanism.md находка 3 и мера (c). За два часа около двадцати процессов opencode serve и 22 дочерних beacon mcp, владелец убил вручную; предыдущий инцидент 37 серверов и 29 сирот. В старом коде не было Pdeathsig, записи PID и проверки живости; gc не был подключён к dispatch. Базовый замер: ~20 процессов за 2 часа, порог тревоги больше двух живых серверов. Базовый замер и приоритет из отчёта: P0.
Шаги: Проверить текущий main: коммит PR 887 добавил Pdeathsig в server.go:118, запись opencode-serve.json (:193-269), переиспользование живого PID (:390-429) и gc в dispatch. Если это уже есть, ограничить работу настройкой регулярного запуска beacon gc --apply через cron или timer с интервалом 10 минут. Перед включением --apply выполнить dry run и сверить список. GC должен пропускать PID и порт из service.json и opencode-serve.json. Добавить отчёт о числе сирот в ночную смену.
Проверка: CI зелёный. В конфигурации владельца есть timer или cron. Dry run gc не помечает защищённые PID. Проверка после недели работы показывает ноль сирот.
Приёмка: Сироты убираются автоматически. Порог тревоги срабатывает при более чем двух живых серверах.

Итог: Already landed in PR #1339 (commit 05e0018b19864cc171bd45b01ac0e2ada3ea0ebf). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1339 merged 2026-11-22T09:10:44+05:00; commit 05e0018b19864cc171bd45b01ac0e2ada3ea0ebf; git merge-base --is-ancestor 05e0018b19864cc171bd45b01ac0e2ada3ea0ebf origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1339 05e0018b
Время закрытия: 2026-11-22T11:24:28Z

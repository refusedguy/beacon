# BCX-25. Ввести единый --format для всех команд
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/logs.go, cmd/beacon/inspect.go, cmd/beacon/observe.go, cmd/beacon/catalog.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать три механизма выбора формата и разную семантику --json.
Контекст: Источник: docs/research/cli-ux-audit.md, таблица 5 строка 5. table|json используют list, agents, deferred, workload, watch, gc, ci check, pr create, orchestrate, registry list, task list; json|prometheus только metrics (observe.go:123); --json вместо --format у logs (logs.go:27) и inspect (inspect.go:105). logs --format json даёт flag provided but not defined, код 2. Базовый замер и приоритет из отчёта: P1.
Шаги: Определить один --format с перечислением значений на команду. Оставить --json псевдонимом без второй семантики. Принять --format в logs и inspect. Добавить тест на все значения формата по командам.
Проверка: CI зелёный. logs --format json и inspect --format json парсятся. Тест перебирает команды и их допустимые значения.
Приёмка: Один флаг выбирает формат во всех командах. --json означает одно и то же.

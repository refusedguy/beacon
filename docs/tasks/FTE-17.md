# FTE-17. Прочитать секцию paths из конфига артефактов
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/app/orchestrator/checkall.go, internal/app/orchestrator/checkall_test.go, cmd/beacon/orchestrate.go
Зависимости:
Ссылка: docs/templates/executor-task.md:856-875, docs/templates/orchestrator-task.md:1105-1116, docs/evals/hd25-config-paths.md:1638-1653
Агент: ses_fte17_executor
Время: 2026-11-22T18:18:49Z
baseline: Config в internal/app/orchestrator/checkall.go:98 содержит MaxRebases, Repo, OrchestratorID, SlotsTotal, WakeTarget и Flaky. Поля Paths, paths.docs, paths.tasks и paths.evals отсутствуют. HD-052 закрыта и меняла только docs/templates, код конфига не трогала.
Цель: Исполнитель и оркестратор берут пути docs, tasks и evals из одного конфига с дефолтами и ошибкой на неизвестный ключ.
Контекст: docs/templates/executor-task.md:862 задаёт источник .beacon/config.yaml, секцию paths и три ключа с дефолтами docs, docs/tasks и evals. Строка 868 объявляет неизвестный ключ и пустое значение ошибкой. docs/templates/orchestrator-task.md:1107 требует тот же источник для оркестратора и называет дубль пути провалом карточки HD-052. Сейчас код читает конфиг, но секции paths нет, поэтому шаблоны обещают поведение, которого нет в бинарнике.
Шаги: Добавить тип Paths с полями Docs, Tasks и Evals в Config. Задать дефолты из таблицы шаблона. Отклонять неизвестный вложенный ключ и пустое значение. Использовать значения из конфига там, где оркестратор читает карточки и eval-артефакты. Покрыть тестом отсутствия файла, неполной секции и лишнего ключа.
Проверка: При отсутствии config.yaml действуют дефолты docs, docs/tasks и evals. Конфиг с paths.tasks: custom/tasks меняет путь чтения карточек. Неизвестный ключ paths.foo даёт ошибку с именем поля. Пустое значение paths.docs даёт ошибку.
Приёмка: Оба шаблона соответствуют коду, CI зелёный.

Причина: already_landed
Закрыл: beacon
Время закрытия: 2026-11-22T18:25:33Z
## Подтверждение
- PR #2130 MERGED: 2026-11-22 (squash).

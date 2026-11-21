# IDX-14. Обновление AGENTS.md, README и шаблонов
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: AGENTS.md, README.md, docs/templates/executor-task.md, docs/templates/orchestrator-task.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Инструкции учат новой модели: index, claim, board, граф без циклов.
Контекст: Premise: статические тексты не обновятся сами, агенты продолжат писать старые пути.
Шаги: Заменить описания источников задач, добавить beacon_task_claim и beacon board, убрать устаревшие ссылки на docs/tasks как единственный путь.
Проверка: gofmt не aplicable, docs gate в CI.
Приёмка: Ни одна инструкция не учит устаревшему потоку.

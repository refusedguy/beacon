# IDX-14. Обновление AGENTS.md, README и шаблонов
Статус: done
Фаза: P2
Тип: task
Владелец файлов: AGENTS.md, README.md, docs/templates/executor-task.md, docs/templates/orchestrator-task.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Инструкции учат новой модели: index, claim, board, граф без циклов.
Контекст: Premise: статические тексты не обновятся сами, агенты продолжат писать старые пути.
baseline: 4 OwnerFiles учили старому потоку. После правки сохранены все маркерные строки шаблонов (21 executor-task, 8 orchestrator-task) и проходят 10 адаптер-тестов T3.7 и T3.8.
Шаги: Заменить описания источников задач, добавить beacon_task_claim и beacon board, убрать устаревшие ссылки на docs/tasks как единственный путь.
Проверка: gofmt не aplicable, docs gate в CI.
Приёмка: Ни одна инструкция не учит устаревшему потоку.

## Подтверждение
- Premise проверен 2026-11-21: статические тексты действительно учили старому потоку. Обновлены все четыре OwnerFiles: AGENTS.md, README.md, docs/templates/executor-task.md и docs/templates/orchestrator-task.md. Инструкции теперь описывают индекс и граф без циклов, `beacon_task_claim`, `beacon board` и автоматический перевод карточки в `done` при `merged`. Ссылка на `docs/tasks/` как единственный путь к задаче убрана. Маркеры адаптер-тестов T3.7 и T3.8 сохранены, gofmt не aplicable, проверяет docs gate в CI.

# ATX-D07. Починить ссылки на отсутствующий research-файл в runbook
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: docs/runbooks/BLOCKER-CLOSE-PLAN.md, docs/adrs/0270-task-close-tool.md
Зависимости: ATX-D02
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать ссылки на research-файл, которого нет в дереве.
Контекст: Источник: docs/research/docs-consistency-gaps.md, находка ATX-D07. `docs/runbooks/BLOCKER-CLOSE-PLAN.md:3` и `:114` называют источником `docs/research/task-lifecycle-brainstorm.md`. Этого файла нет в git. На него же ссылается `docs/adrs/0270-task-close-tool.md`. Остальные четыре runbook ссылаются на существующие файлы. Проверка путей внутри backticks появится в ATX-D02.
Шаги: Заменить ссылку на существующий источник, например `docs/runbooks/CLOSE-PROCEDURE.md` и карточки с полем Подтверждение. В ADR 0270 заменить или убрать ту же ссылку, сохранив смысл абзаца. Использовать правило из ATX-D02 для контроля.
Проверка: CI зелёный. В `docs/runbooks/` и `docs/adrs/0270-task-close-tool.md` нет ссылок на `task-lifecycle-brainstorm.md`. Новое правило gate не находит finding в этих файлах.
Приёмка: Runbook ссылается только на файлы из дерева. Источник утверждения о процедуре закрытия указан явно.

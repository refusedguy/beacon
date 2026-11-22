# ATX-D07. Починить ссылки на отсутствующий research-файл в runbook
Статус: done
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

Итог: Already landed in PR #1640 (commit 4a0d33b5f661480b0b9a4ebac3875d2934d61642): docs(atx-d07): убрать ссылки на отсутствующий research-файл
Доказательство: PR #1640; commit 4a0d33b5f661480b0b9a4ebac3875d2934d61642; git merge-base --is-ancestor 4a0d33b5f661480b0b9a4ebac3875d2934d61642 origin/main exit 0; No reference to task-lifecycle-brainstorm.md remains in docs/runbooks or docs/adrs/0270-task-close-tool.md.
Причина: already landed in PR #1640 4a0d33b5f661
Время закрытия: 2026-11-22T11:53:03Z

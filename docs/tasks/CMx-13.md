# CMx-13. Handoff-документ свежему агенту во временном каталоге
Статус: done
Фаза: P2
Тип: doc
Владелец файлов: docs/runbooks/handoff.md
Зависимости:
Ссылка: docs/research/comp-matt-skills.md; mattpocock/skills@abeeaf9 skills/productivity/handoff/SKILL.md
Агент: ses_cmx13c_executor
Время: 2026-11-22T18:10:04Z
Цель: Записать в beacon порядок передачи контекста свежему агенту через отдельный документ вне рабочего дерева.
Контекст: Источник: `skills/productivity/handoff/SKILL.md:8-15` требует сохранить документ во временном каталоге ОС `$TMPDIR` или `/tmp`, а не в текущем workspace, добавить секцию suggested skills с именами скиллов для следующего агента, не дублировать содержание spec, планов, ADR, issue и коммитов, а ссылаться на них по пути или URL, и вырезать секреты. baseline по `origin/main` `0ce4ba29`: `git grep -i -c -E 'handoff document|suggested skills|\$TMPDIR' -- AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/design docs/adrs internal cmd tools scripts` даёт 0. Отдельный поиск `tmpdir` даёт 22 совпадения в `.opencode/plugins/beacon-bridge.js`, все это вызовы Node `tmpdir()` для временных каталогов тестов, а не практика handoff-документа. `AGENTS.md:121-129` описывает перезапуск после смерти через git status, ключ `beacon/survival-rules` и продолжение в том же worktree, но не описывает передачу знаний другому агенту. `docs/templates/executor-task.md:150-154` упоминает handoff в ответе claim, не определяя его форму. CMx-12 описывает интерактивный скрипт, не документ.
Шаги: Создать `docs/runbooks/handoff.md`. Записать, когда используется handoff, а когда достаточно перезапуска из `AGENTS.md:121-129`. Указать путь во временном каталоге ОС, обязательную секцию suggested skills, правило ссылаться на артефакты вместо копирования и правило вырезать секреты. Добавить требование передавать задачу через `beacon_task_claim`, а не через сам документ.
Проверка: Файл существует, содержит все четыре требования и указатель на `AGENTS.md:121-129`. `git grep -c 'suggested skills' -- docs/runbooks/handoff.md` больше 0; `git grep -c 'TMPDIR' -- docs/runbooks/handoff.md` больше 0. Код не тронут.
Приёмка: Документ без секции suggested skills или с требованием положить файл в репозиторий проваливает карточку. Измерение до выбора зафиксировано нулевым поиском.

Причина: Fix already landed in origin/main via PR #1856 (commit 8034ac8e 'docs(runbooks): add handoff runbook for cross-agent context transfer (CMx-13) (#1856)'). docs/runbooks/handoff.md exists in main; git merge-base --is-ancestor 8034ac8e origin/main exit 0. No duplicate code PR; this closes the stale todo card.
Закрыл: ses_cmx13c_executor
Время закрытия: 2026-11-22T18:11:00Z
## Подтверждение
- PR #2090 MERGED: 2026-11-22 (squash).


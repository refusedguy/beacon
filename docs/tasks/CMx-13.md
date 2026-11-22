# CMx-13. Handoff-документ свежему агенту во временном каталоге
Статус: todo
Фаза: P2
Тип: doc
Владелец файлов: docs/runbooks/handoff.md
Зависимости:
Ссылка: docs/research/comp-matt-skills.md; mattpocock/skills@abeeaf9 skills/productivity/handoff/SKILL.md
Агент: нет
Время: нет
Цель: Записать в beacon порядок передачи контекста свежему агенту через отдельный документ вне рабочего дерева.
Контекст: Источник: `skills/productivity/handoff/SKILL.md:8-15` требует сохранить документ во временном каталоге ОС `$TMPDIR` или `/tmp`, а не в текущем workspace, добавить секцию suggested skills с именами скиллов для следующего агента, не дублировать содержание spec, планов, ADR, issue и коммитов, а ссылаться на них по пути или URL, и вырезать секреты. baseline по `origin/main` `0ce4ba29`: `git grep -i -c -E 'handoff document|suggested skills|\$TMPDIR' -- AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/design docs/adrs internal cmd tools scripts` даёт 0. Отдельный поиск `tmpdir` даёт 22 совпадения в `.opencode/plugins/beacon-bridge.js`, все это вызовы Node `tmpdir()` для временных каталогов тестов, а не практика handoff-документа. `AGENTS.md:121-129` описывает перезапуск после смерти через git status, ключ `beacon/survival-rules` и продолжение в том же worktree, но не описывает передачу знаний другому агенту. `docs/templates/executor-task.md:150-154` упоминает handoff в ответе claim, не определяя его форму. CMx-12 описывает интерактивный скрипт, не документ.
Шаги: Создать `docs/runbooks/handoff.md`. Записать, когда используется handoff, а когда достаточно перезапуска из `AGENTS.md:121-129`. Указать путь во временном каталоге ОС, обязательную секцию suggested skills, правило ссылаться на артефакты вместо копирования и правило вырезать секреты. Добавить требование передавать задачу через `beacon_task_claim`, а не через сам документ.
Проверка: Файл существует, содержит все четыре требования и указатель на `AGENTS.md:121-129`. `git grep -c 'suggested skills' -- docs/runbooks/handoff.md` больше 0; `git grep -c 'TMPDIR' -- docs/runbooks/handoff.md` больше 0. Код не тронут.
Приёмка: Документ без секции suggested skills или с требованием положить файл в репозиторий проваливает карточку. Измерение до выбора зафиксировано нулевым поиском.

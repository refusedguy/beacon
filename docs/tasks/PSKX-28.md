# PSKX-28. Минимальный diff и одно решение на источник
Статус: done
Фаза: P8
Тип: doc
Владелец файлов: AGENTS.md
Зависимости: 
Ссылка: https://raw.githubusercontent.com/cursor/plugins/main/pstack/skills/principle-laziness-protocol/SKILL.md
Агент: ses_pskx28e_executor
Время: 2026-11-22T18:42:26Z
Цель: Не повторять одно решение в нескольких местах, держать его за одним источником ипередавать результат простым флагом; предпочитать наименьшее изменение, решающее задачу.
Контекст: Источник: `pstack/skills/principle-laziness-protocol/SKILL.md:9-10` (Minimize the diff). `AGENTS.md:61-67` требует свою ветку и `git diff --stat` перед PR, но не задаёт.portог размера diff. CM-04 описывает вертикальные срезы и expand-contract, а не правило одного источника решения. baseline: `git grep -i -E 'минимальный diff|одно решение' -- AGENTS.md | wc -l` = 0 на `origin/main`. Карточка фиксирует приём в дереве beacon, не создаёт новый глобальный скилл и не меняет протокол Watch. Не дублирует PSK-01 (кандидаты), PSK-02 (адверсарии), PSK-03 (TLA Watch), CP-01..05, CM-01..05 или закрытые дубли HD-099/HD-105/HD-109. Риск: низкий.
Шаги: Добавить в раздел «Своя ветка и PR» правило «Минимальный diff»: каждое повторное решение вынести в один источник и передать результат флагом; перед PR сверить `git diff --stat` и объяснить каждую строку, не относящиеся к задаче. Запретить добавлять абстракции ради будущих Extension.
Проверка: `git grep -n -E 'минимальный diff|одно решение' AGENTS.md` находит правило и проверку перед PR.
Приёмка: В PR нет повторного решения и абстракций без текущего вызывающего. `git diff --stat` приложен к отчёту.

Причина: Already landed in origin/main: full SHA 9a0038c7 docs(agents): PSKX-28 minimal diff, one decision per source (#1942); git merge-base --is-ancestor 9a0038c7 origin/main exit 0; AGENTS.md change present, no duplicate code PR.
Закрыл: ses_pskx28e_executor
Время закрытия: 2026-11-22T18:42:31Z
## Подтверждение
- PR #2166 MERGED: 2026-11-22 (squash).


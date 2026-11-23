# CMx-10. Карта решений: decision-тикеты, claim до работы и research-ветка без PR
Статус: done
Фаза: P3
Тип: doc
Владелец файлов: docs/design/decision-map.md
Зависимости:
Ссылка: docs/research/comp-matt-skills.md; mattpocock/skills@abeeaf9 skills/engineering/wayfinder/SKILL.md
Агент: ses_cmx10b_executor
Время: 2026-11-22T18:07:44Z
Цель: Зафиксировать в beacon приём планирования большого туманного объёма через карту решений, а не через граф исполнительных карточек.
Контекст: Источник: `skills/engineering/wayfinder/SKILL.md:7-13` отличает decision ticket от исполняемой задачи и требует производить решения, а не поставку. Строки `:21-23` задают map как единственный issue-индекс, который не хранит детали, а ссылается на ticket. Строка `:67` требует взять тикет в работу назначением до начала любой работы, чтобы параллельные сессии его пропускали. Строки `:69-71` задают frontier из открытых, разблокированных, не взятых тикетов и блокировку по нативным связям трекера. Строки `:82-101` различают fog of war и out of scope и требуют не нарезать туман заранее. Строки `:103-116` задают не более одного тикета за сессию и research-сабагентов на временной ветке `research/<name>` без PR. baseline по `origin/main` `0ce4ba29`: `git grep -i -c -E 'decision ticket|fog of war|карта решений' -- AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/design docs/adrs internal cmd tools scripts` даёт 0. `AGENTS.md:72-80` описывает граф из blocks, depends-on, supersedes и related и frontier по задачам, но не различает решения и поставку и не описывает research-ветки без PR. CM-04 обсуждает нарезку исполняемых карточек, этот приём другой.
Шаги: Создать `docs/design/decision-map.md`. Записать различие между decision ticket и исполнительной карточкой. Описать map как индекс, где решение живёт в тикете, обязательный claim до работы, frontier по нативным блокировкам и правило одно решение за сессию. Отдельно зафиксировать статьи Fog of war и Out of scope и правило research-ветки `research/<name>` без PR. Дать указатель на `AGENTS.md:72-80`, не меняя граф.
Проверка: Файл существует, содержит все шесть элементов. `git grep -c 'decision ticket' -- docs/design/decision-map.md` больше 0; `git grep -c 'research/' -- docs/design/decision-map.md` больше 0. Код не тронут.
Приёмка: Отсутствие различия между решением и поставкой или отсутствие правила claim до работы проваливает карточку. Измерение до выбора зафиксировано нулевым поиском.

Причина: Уже влито в origin/main через PR #1853 (merge commit dea78a96, исходный коммит a3328e6c). `git merge-base --is-ancestor dea78a96 origin/main` вернул код выхода 0. docs/design/decision-map.md уже присутствует на main и содержит все шесть элементов из проверки карточки. PR-дубль кода не открывается по ветке 6b.
Закрыл: ses_cmx10b_executor
Время закрытия: 2026-11-22T18:08:24Z
## Подтверждение
- PR #2075 MERGED: 2026-11-22 (squash).


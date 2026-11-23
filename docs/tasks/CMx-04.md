# CMx-04. Проектирование интерфейса тремя расходящимися вариантами
Статус: done
Фаза: P3
Тип: doc
Владелец файлов: docs/design/design-it-twice.md
Зависимости:
Ссылка: docs/research/comp-matt-skills.md; mattpocock/skills@abeeaf9 skills/engineering/codebase-design/SKILL.md; skills/engineering/codebase-design/DESIGN-IT-TWICE.md
Агент: ses_cmx04b_executor
Время: 2026-11-22T17:06:58Z
Цель: Зафиксировать приём параллельного проектирования нескольких радикально разных интерфейсов до выбора одного, отдельно от критериев глубины из CM-03.
Контекст: Источник: `skills/engineering/codebase-design/SKILL.md:114` отправляет к DESIGN-IT-TWICE, когда нужно сравнить альтернативные интерфейсы. `skills/engineering/codebase-design/DESIGN-IT-TWICE.md:19-30` требует 3 или более параллельных сабагентов в свежих окнах, каждый получает отдельный технический бриф и своё ограничение: минимум интерфейса 1-3 точки входа, максимум гибкости, оптимизация под самого частого вызывающего, порты и адаптеры для межсеамных зависимостей. Строки `:32-38` задают пять обязательных выходов сабагента: интерфейс с инвариантами и режимами ошибок, пример вызова, что спрятано за seam, стратегия зависимостей и адаптеров, компромиссы. Строки `:40-44` требуют сравнения по depth, locality и размещению seam и обязательной авторской рекомендации, а не меню. baseline по `origin/main` `0ce4ba29`: `git grep -i -c -E 'design it twice|DESIGN-IT-TWICE|radically different' -- AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/design docs/adrs internal cmd tools scripts` даёт 0. CM-03 описывает deletion test, число адаптеров и негативный контроль, но не описывает генерацию альтернатив. `AGENTS.md:139-149` спавнит агентов по свободным слотам, а не по конкурирующим интерфейсам.
Шаги: Создать `docs/design/design-it-twice.md`. Описать подготовку problem space с ограничениями и наброском до спавна, три или четыре брифа с разными ограничениями, пять выходов каждого сабагента и сравнение по трём осям. Указать, что результат это рекомендация с объяснением или гибрид, а не список на выбор. Дать указатель на CM-03 как на критерий оценки глубины и не повторять его проверки.
Проверка: Файл существует, содержит все четыре ограничения, все пять выходов и три оси сравнения. `git grep -c 'DESIGN-IT-TWICE' -- docs/design/design-it-twice.md` больше 0. Код не тронут.
Приёмка: Меньше трёх альтернатив, отсутствие оси depth/locality/seam или отсутствие рекомендации проваливает карточку. Измерение до выбора зафиксировано нулевым поиском.

Причина: Already landed in origin/main via PR #1838, full SHA 953187b0080a24d0d8e1ff20c50a3c990e9ee0e3 (docs(design): add design-it-twice parallel interface technique (CMx-04)); git merge-base --is-ancestor 953187b0080a24d0d8e1ff20c50a3c990e9ee0e3 origin/main exit 0; docs/design/design-it-twice.md present in origin/main. No duplicate code written.
Закрыл: ses_cmx04b_executor
Время закрытия: 2026-11-22T17:07:30Z
## Подтверждение
- PR #2005 MERGED: 2026-11-22 (squash).


# CMx-07. Отчёт по кандидатам на углубление вне дерева с силой рекомендации
Статус: todo
Фаза: P3
Тип: doc
Владелец файлов: docs/templates/architecture-report.md
Зависимости:
Ссылка: docs/research/comp-matt-skills.md; mattpocock/skills@abeeaf9 skills/engineering/improve-codebase-architecture/SKILL.md
Агент: нет
Время: нет
Цель: Зафиксировать формат отчёта по архитектурным кандидатам, который не оставляет временные файлы в репозитории и помечает силу каждой рекомендации.
Контекст: Источник: `skills/engineering/improve-codebase-architecture/SKILL.md:37-41` требует записать самодостаточный HTML-файл во временный каталог ОС, взять `$TMPDIR` с fallback на `/tmp`, имя вида `architecture-review-<timestamp>.html`, открыть файл и сообщить абсолютный путь. Там же предписаны Tailwind и Mermaid через CDN и отдельная визуализация до и после для каждого кандидата. Строки `:43-52` задают карточку кандидата из шести полей: Files, Problem, Solution, Benefits в терминах locality и leverage, Before/After diagram и Recommendation strength из трёх значений Strong, Worth exploring, Speculative. Там же нужен финальный раздел Top recommendation. Строки `:54-60` требуют vocabulary из `GLOSSARY.md` и codebase-design, пометку о конфликте с ADR и запрет предлагать интерфейсы до выбора кандидата. baseline по `origin/main` `0ce4ba29`: `git grep -i -c -E 'recommendation strength|architecture-review-|Worth exploring' -- AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/design docs/adrs internal cmd tools scripts` даёт 0. Архитектурные находки в `docs/research/` пишутся в сам репозиторий, силы рекомендации и единого шаблона нет.
Шаги: Создать `docs/templates/architecture-report.md` с каркасом карточки кандидата и шестью обязательными полями. Записать правило писать отчёт во временный каталог ОС и не коммитить его, а в репозиторий класть только итоговую карточку задачи. Зафиксировать три значения силы рекомендации и финальный Top recommendation. Добавить требование помечать конфликт с ADR и не переходить к интерфейсам до выбора кандидата, с указателем на CMx-04.
Проверка: Файл существует, содержит шесть полей, три значения силы и правило записи во временный каталог. `git grep -c 'Worth exploring' -- docs/templates/architecture-report.md` больше 0; `git grep -c 'TMPDIR' -- docs/templates/architecture-report.md` больше 0. Код не тронут.
Приёмка: Отсутствие силы рекомендации, поля Benefits или правила о временном каталоге проваливает карточку. Измерение до выбора зафиксировано нулевым поиском.

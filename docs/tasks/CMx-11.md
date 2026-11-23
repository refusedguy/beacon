# CMx-11. Ретроспектива: механические ошибки превращаются в проверки
Статус: done
Фаза: P2
Тип: doc
Владелец файлов: docs/runbooks/retro.md
Зависимости:
Ссылка: docs/research/comp-matt-skills.md; mattpocock/skills@abeeaf9 skills/engineering/retro/SKILL.md
Агент: ses_cmx11b_executor
Время: 2026-11-22T18:07:53Z
Цель: Задать в beacon порядок разбора прошедшей сессии, при котором механическая ошибка становится детерминированной проверкой, а AGENTS.md остаётся указателем.
Контекст: Источник: `skills/engineering/retro/SKILL.md:15-23` перечисляет категории поиска: навигация, автоматические проверки, стандарты кода, глобальный AGENTS.md, экономия инструментов, no-op и доступ к информации. Строка `:18` требует сначала прочитать собственную команду проверки репозитория и считать находкой уже существующий, но не подключённый гейт. Строка `:19` делит нарушения на механические и решения: механическое получает детерминированную проверку в линтере, хуке или CI, а CODING_STANDARDS.md остаётся только для judgement call. Строки `:29-43` объясняют, что reviewer, а не implementer, навязывает стандарты, и что AGENTS.md и CLAUDE.md должны быть указателями, а CODING_STANDARDS.md читается на ревью. baseline по `origin/main` `0ce4ba29`: `git grep -i -c -E 'retro|навигационн.*указатель|CODING_STANDARDS' -- AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/design docs/adrs internal cmd tools scripts` даёт 0. `AGENTS.md:161-163` содержит только правило unslop, а `AGENTS.md:5-19` уже накопил таблицу запретов без процедуры их пересмотра.
Шаги: Создать `docs/runbooks/retro.md`. Записать категории поиска и правило сначала искать существующий гейт. Добавить разделение: механическая ошибка превращается в lint, hook или CI-проверку, judgement call в стандарт ревью. Описать, что AGENTS.md остаётся коротким указателем, а подробности уходят в отдельные документы. Дать указатель на таблицу запретов в `AGENTS.md:5-19`, не меняя её в этой карточке.
Проверка: Файл существует, содержит семь категорий, разделение на механическое и решение и правило о существующем гейте. `git grep -c 'CODING_STANDARDS' -- docs/runbooks/retro.md` больше 0. Код не тронут.
Приёмка: Отсутствие деления на механическое и решение или отсутствие требования прочитать существующий гейт проваливает карточку. Измерение до выбора зафиксировано нулевым поиском.

Причина: Already merged via PR #1852, commit 091a00f2f78988754f14417451b4bbecbfa08c0d (docs(runbooks): add retro runbook turning mechanical errors into checks (CMx-11)). git merge-base --is-ancestor 091a00f2f78988754f14417451b4bbecbfa08c0d origin/main exit 0; docs/runbooks/retro.md present in origin/main. No duplicate code written.
Закрыл: ses_cmx11b_executor
Время закрытия: 2026-11-22T18:08:16Z
## Подтверждение
- PR #2078 MERGED: 2026-11-22 (squash).


# CMx-18. Разовая настройка репозитория под скиллы: explore, present, confirm, write
Статус: done
Фаза: P3
Тип: doc
Владелец файлов: docs/runbooks/repo-setup.md
Зависимости:
Ссылка: docs/research/research-matt-skills.md; mattpocock/skills@abeeaf9 skills/engineering/setup-matt-pocock-skills/SKILL.md
Агент: ses_cmx18b_executor
Время: 2026-11-22T18:21:17Z
Цель: Дать beacon порядок разовой настройки репозитория, чтобы конфиг трекера, словаря и меток лежал в дереве, а не в голове у агента.
Контекст: Источник: `skills/engineering/setup-matt-pocock-skills/SKILL.md:9-15` задаёт три артефакта: issue tracker, triage labels и domain docs, и прямо называет процедуру prompt-driven: explore, present, confirm, write, а не детерминированный скрипт. Строки `:19-30` требуют прочитать `git remote`, AGENTS.md, GLOSSARY.md, каталоги ADR и `.scratch/`, не предполагая ничего заранее. Строки `:32-36` требуют показывать находки и задавать по одному вопросу за раз, начиная с рекомендованного ответа. Строки `:74-104` задают блок `## Agent skills` в уже существующем AGENTS.md или CLAUDE.md и три файла `docs/agents/*.md`, обновлять блок на месте, а не добавлять дубль. baseline по `origin/main` `0ce4ba29`: `git grep -i -c -E 'Agent skills|docs/agents/' -- AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/design docs/adrs internal cmd tools scripts` даёт 2 совпадения в `docs/adrs/0035-o-claude-code-mechanics-features.md:21` и `:24`, где идёт речь о формате Agent Skills и о профиле сабагента, и 0 файлов в `docs/agents/`. В `AGENTS.md` раздела `## Agent skills` нет.
Шаги: Создать `docs/runbooks/repo-setup.md`. Записать четыре фазы и список того, что читается на phase explore. Описать конфиг трекера beacon как tasks в `docs/tasks/` и `beacon board` вместо GitHub Issues, а также отображение пяти состояний из CMx-09 на существующие статусы карточек. Зафиксировать, что блок в AGENTS.md обновляется на месте и что файлы `docs/agents/` создаются только при реальной потребности. Не менять AGENTS.md и ADR 0035.
Проверка: Файл существует, содержит все четыре фазы, список файлов explore и правило обновления блока на месте. `git grep -c 'docs/agents' -- docs/runbooks/repo-setup.md` больше 0. Код не тронут.
Приёмка: Отсутствие фазы confirm или попытка создать дубль раздела `## Agent skills` проваливает карточку. Измерение до выбора зафиксировано одним совпадением в ADR и нулём файлов.

Причина: already_landed
Закрыл: beacon
Время закрытия: 2026-11-22T18:23:10Z
## Подтверждение
- PR #2126 MERGED: 2026-11-22 (squash).

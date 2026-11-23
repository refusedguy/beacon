# CMx-09. Состояния триажа и проверка утверждения до расспроса
Статус: done
Фаза: P2
Тип: doc
Владелец файлов: docs/design/triage-states.md
Зависимости:
Ссылка: docs/research/comp-matt-skills.md; mattpocock/skills@abeeaf9 skills/engineering/triage/SKILL.md
Агент: ses_cmx09b_executor
Время: 2026-11-22T17:41:00Z
Цель: Дать ночной смене beacon письменный набор состояний триажа, правило проверять утверждение до расспроса и шаблон brief для агента.
Контекст: Источник: `skills/engineering/triage/SKILL.md:24-45` задаёт две категории bug и enhancement и пять состояний needs-triage, needs-info, ready-for-agent, ready-for-human, wontfix. Каждая задача несёт ровно одну категорию и одно состояние, конфликт состояний требует вопроса владельцу. Строки `:68-76` требуют перед расспросом собрать контекст, проверить утверждение: для бага воспроизвести по шагам заявителя, для PR подтвердить, что diff делает заявленное, и только потом переходить к grilling. Строки `:78-86` задают три разные причины wontfix: уже реализовано, отклонённый баг, отклонённое улучшение с записью в `.out-of-scope/`. baseline по `origin/main` `0ce4ba29`: `git grep -i -c -E 'needs-triage|ready-for-agent|agent brief|wontfix' -- AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/design docs/adrs internal cmd tools scripts` даёт 0. `.opencode/skills/night-shift/SKILL.md:8-15` содержит пять шагов разбора issue, но в нём нет состояний, проверки утверждения и трёх причин закрытия. research-matt-skills.md отнёс triage к установленному скиллу, но контракт в дереве отсутствует.
Шаги: Создать `docs/design/triage-states.md`. Перечислить две категории и пять состояний с правилом «одна категория и одно состояние». Добавить проверку утверждения до расспроса с тремя исходами: подтверждено с указанием кодового пути, не подтвердилось, недостаточно деталей. Записать три причины wontfix и разные действия для каждой. Дать указатель на `.opencode/skills/night-shift/SKILL.md:8-15` и его справочники, не меняя скилл.
Проверка: Файл существует, содержит все семь ролей, три исхода проверки и три причины wontfix. `git grep -c 'ready-for-agent' -- docs/design/triage-states.md` больше 0. Код не тронут.
Приёмка: Отсутствие проверки утверждения или слияние трёх причин wontfix в одну проваливает карточку. Измерение до выбора зафиксировано нулевым поиском по четырём терминам.

Причина: Already landed in PR #1855, merge commit fad36274ffeae3efc5642b177d5596c11233fb5e (docs(design): triage states and claim check (CMx-09)). docs/design/triage-states.md is present in origin/main; git merge-base --is-ancestor fad36274ffeae3efc5642b177d5596c11233fb5e origin/main exits 0. No duplicate code written.
Закрыл: ses_cmx09b_executor
Время закрытия: 2026-11-22T17:41:24Z
## Подтверждение
- PR #2053 MERGED: 2026-11-22 (squash).


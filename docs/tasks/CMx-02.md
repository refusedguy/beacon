# CMx-02. Тело PR как запись о риске
Статус: done
Фаза: P2
Тип: doc
Владелец файлов: docs/templates/pr-body.md
Зависимости:
Ссылка: docs/research/comp-matt-skills.md; docs/research/research-matt-skills.md; mattpocock/skills@abeeaf9 skills/engineering/pr/SKILL.md
Агент: ses_cmx02b_executor
Время: 2026-11-22T16:54:37Z
Цель: Дать проекту шаблон тела PR с минимальной визуализацией, доказательством до и после и явной оценкой обратимости merge.
Контекст: Источник: `skills/engineering/pr/SKILL.md:12-33` задаёт каркас из четырёх part: Summary с диаграммой, diff-sketch или деревом, Evidence с парами Before и After, Merge Danger с полями Door и Blast Radius. Строки `:39-93` перечисляют допустимые визуализации и требовать smallest view, который делает точку ясной. Строки `:158-170` уточняют Evidence: скриншот это S-tier, результат исполнения A-tier, нужен точный тест, который падал и стал проходить. Там же Door это one-way или two-way: two-way можно откатить, one-way нельзя, и blast radius описывает потенциальный масштаб последствий. baseline по `origin/main` `0ce4ba29`: `git grep -i -c -E 'Merge Danger|blast radius|one-way door|two-way door' -- AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/design docs/adrs internal cmd tools scripts` даёт 0. `AGENTS.md:161-163` требует только прогона unslop для прозы PR, а `docs/templates/executor-task.md:165-171` проверяет наличие документации в PR, но не его тело. Шаблона тела PR в дереве нет. research-matt-skills.md отмечает этот приём как кандидата без карточки, CMx-02 его снимает.
Шаги: Создать `docs/templates/pr-body.md` с разделами Summary, Evidence, Merge Danger. В Evidence требовать пару Before и After и либо вывод падавшего теста, либо вывод прошедшего теста с указанием команды. В Merge Danger требовать явный выбор Door и поле Blast Radius. Добавить правило: визуализация выбирается минимальной по объёму, преамбулы и 공간ные слова запрещены, термины берутся из `GLOSSARY.md`. Ссылку на шаблон добавить только в своём новом документе, `AGENTS.md` и README не править.
Проверка: Файл существует, содержит все четыре раздела и оба поля Merge Danger. `git grep -c 'Blast Radius' -- docs/templates/pr-body.md` больше 0; `git grep -c 'Before' -- docs/templates/pr-body.md` больше 0. Проверка не запускает go test, go build и beacon gates.
Приёмка: Шаблон без поля Door или без пары Before и After проваливает карточку. Измерение до выбора зафиксировано нулевым поиском по четырём терминам.

Причина: Premise-check: требование уже влито в origin/main через PR #1841, merge commit 251371eae71bc17f25f49c8b99f5236f6ea637fb (docs(templates): add PR body risk record template (CMx-02)). docs/templates/pr-body.md присутствует в origin/main. git merge-base --is-ancestor 251371ea origin/main -> exit 0. gh pr view 1841: state MERGED, mergedAt 2026-11-22T15:35:22Z. Дубль кода не создаём; отдельный docs-PR несёт только статус карточки и доказательство.
Закрыл: ses_cmx02b_executor
Время закрытия: 2026-11-22T16:55:06Z
## Подтверждение
- PR #1970 MERGED: 2026-11-22 (squash).


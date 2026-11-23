# CMx-16. Критерии завершения, no-op и окружение как источник истины в steering-файлах
Статус: done
Фаза: P2
Тип: doc
Владелец файлов: docs/design/agent-docs-rules.md
Зависимости:
Ссылка: docs/research/comp-matt-skills.md; mattpocock/skills@abeeaf9 skills/productivity/writing-for-agents/SKILL.md
Агент: ses_cmx16b_executor
Время: 2026-11-22T18:17:14Z
Цель: Дать beacon правила редактирования AGENTS.md и doc-указателей, чтобы каждый шаг заканчивался проверяемым критерием, а документ не дублировал окружение.
Контекст: Источник: `skills/productivity/writing-for-agents/SKILL.md:45-52` требует, чтобы каждый step заканчивался completion criterion, по которому агент отличает done от not-done. Строки `:47-50` называют premature completion и требуют сначала заострить границу, а уже потом прятать последующие шаги за контекстную границу. Строки `:76-81` требуют держать каждое значение в одном источнике, считать окружение источником истины и видеть документ, который пересказывает команду или конфиг, как cache со сроком годности, искать no-op по предложению и удалять целое предложение. Строки `:29-43` задают иерархию in-file step, in-file reference, disclosed reference и progressive disclosure. baseline по `origin/main` `0ce4ba29`: `git grep -i -c -E 'completion criterion|leading word|no-op' -- AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/design docs/adrs internal cmd tools scripts` даёт 118 для `no-op`: это код, тесты и ADR, где no-op означает отсутствие эффекта (`docs/adrs/0076-patch-inbox.md:15`, `docs/adrs/0067-omnigraph-wave-aw.md:39`), а не no-op предложение в steering-документе; `completion criterion` и `leading word` дают 0. `AGENTS.md:1-167` это длинный список запретов без критериев завершения шагов. CM-05 трогает только GLOSSARY.md, CM-01 и CM-02 только диагностику и шаблон исполнителя.
Шаги: Создать `docs/design/agent-docs-rules.md`. Записать четыре правила: критерий завершения на каждый шаг, один источник значения, окружение как источник истины и удаление no-op предложений. Добавить иерархию из четырёх уровней и progressive disclosure как защиту иерархии. Привести два примера из `AGENTS.md` без правки самого файла.
Проверка: Файл существует, содержит четыре правила и четыре уровня иерархии. `git grep -c 'completion criterion' -- docs/design/agent-docs-rules.md` больше 0; `git grep -c 'no-op' -- docs/design/agent-docs-rules.md` больше 0. Код не тронут.
Приёмка: Отсутствие критерия завершения или правила об окружении проваливает карточку. Измерение до выбора зафиксировано разделением нулевых и ненулевых поисков.

Причина: Уже влито в origin/main через PR #1860 (merge commit 9f7df68ccc070fea6a19e9f7e10f3ac760acfa8f, docs(design): add agent doc editing rules (CMx-16)). git merge-base --is-ancestor 9f7df68ccc070fea6a19e9f7e10f3ac760acfa8f origin/main дал exit 0. OwnerFile docs/design/agent-docs-rules.md присутствует в origin/main. Дубль кода не создаётся.
Закрыл: ses_cmx16b_executor
Время закрытия: 2026-11-22T18:17:38Z
## Подтверждение
- PR #2111 MERGED: 2026-11-22 (squash).


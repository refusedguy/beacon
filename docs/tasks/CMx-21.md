# CMx-21. Спецификация с решениями о тестировании и seam без путей к файлам
Статус: done
Фаза: P3
Тип: doc
Владелец файлов: docs/templates/spec.md
Зависимости: CMx-05
Ссылка: docs/research/research-matt-skills.md; mattpocock/skills@abeeaf9 skills/engineering/to-spec/SKILL.md
Агент: ses_cmx21_executor
Время: 2026-11-22T18:23:05Z
Цель: Дать beacon шаблон spec, где решения о тестировании и точках проверки фиксируются до карточек, а пути к файлам не попадают в текст.
Контекст: Источник: `skills/engineering/to-spec/SKILL.md:7-19` требует синтезировать spec из уже произошедшего разговора без нового интервью, использовать vocabulary из GLOSSARY.md, набросать seam для тестирования и сверить их с владельцем, предпочесть существующие seam новым и стремиться к одному. Строки `:21-75` задают структуру: Problem Statement, Solution, длинный нумерованный список user stories в форме «как actor, я хочу ..., чтобы ...», Implementation Decisions, Testing Decisions и Out of Scope. Строки `:55-57` запрещают конкретные пути и сниппеты, потому что они быстро устаревают, с исключением для сниппета из прототипа, который кодирует решение точнее прозы. Строки `:59-66` требуют в Testing Decisions описание хорошего теста, список модулей и prior art. baseline по `origin/main` `0ce4ba29`: `git grep -i -c -E 'Testing Decisions|user stor|Implementation Decisions' -- AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/design docs/adrs internal cmd tools scripts` даёт 0. Карточки в docs/tasks содержат поля «Цель», «Контекст», «Шаги», «Проверка», но отдельного шаблона spec с решениями о тестировании в дереве нет. CMx-05 задаёт согласование seam, этот шаблон встраивает его в spec.
Шаги: Создать `docs/templates/spec.md` со всеми семью секциями источника. В секции Testing Decisions сослаться на CMx-05 и требовать перечислить seam с пометкой, что ловит и что пропускает. Добавить правило: пути к файлам и сниппеты не пишутся, кроме decision-rich сниппета из прототипа с пометкой об источнике. Указать, что spec публикуется как набор карточек в `docs/tasks/` через `to-tickets`-правила CM-04.
Проверка: Файл существует, содержит все семь секций, правило о путях и указатель на CMx-05. `git grep -c 'Testing Decisions' -- docs/templates/spec.md` больше 0. Код не тронут.
Приёмка: Секция Testing Decisions или запрет на пути проваливают карточку. Измерение до выбора зафиксировано нулевым поиском по трём терминам.

Причина: Fix already merged: commit e718298f88938944fa1b1f9ac303adc408b7579e (PR #1758 "docs(templates): add spec template with testing decisions (CMx-21)"). git merge-base --is-ancestor e718298f origin/main exited 0. docs/templates/spec.md already on origin/main; no duplicate code PR needed.
Закрыл: ses_cmx21_executor
Время закрытия: 2026-11-22T18:23:22Z
## Подтверждение
- PR #2134 MERGED: 2026-11-22 (squash).


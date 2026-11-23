# CMx-22. Переформулировка непонятного сообщения на языке глоссария
Статус: done
Фаза: P3
Тип: doc
Владелец файлов: docs/runbooks/rephrase.md
Зависимости:
Ссылка: docs/research/comp-matt-skills.md; mattpocock/skills@abeeaf9 skills/productivity/wait-what/SKILL.md
Агент: ses_cmx22_executor
Время: 2026-11-22T18:23:37Z
Цель: Задать в beacon правило для ситуации, когда сообщение владельца или агента не дошло до получателя: пересказывая его проще и на словах домена.
Контекст: Источник: `skills/productivity/wait-what/SKILL.md:3-7` описывает триггер: последнее сообщение не было понято. Требуется stop and re-pitch, добавить немного пропущенного контекста, говорить на ASD-STE100 Simplified Technical English и использовать ubiquitous language из `GLOSSARY.md`, а при нескольких контекстах идти через `GLOSSARY-MAP.md`. `skills/engineering/ask-matt/SKILL.md:91` называет это корректировкой посреди разговора и отличает её от упреждающего лечения общей терминологией. baseline по `origin/main` `0ce4ba29`: `git grep -i -c -E 'Simplified Technical English|re-pitch|GLOSSARY-MAP' -- AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/design docs/adrs internal cmd tools scripts` даёт 0. `.opencode/skills/night-shift/references/comment-rules.md` ограничивает комментарии в issue новым фактом, но не описывает, как переформулировать непонятное сообщение. CMx-20 добавляет активную работу со словарём, этот приём про восстановление понимания внутри разговора.
Шаги: Создать `docs/runbooks/rephrase.md`. Описать триггер и три обязательных элемента переформулировки: пропущенный контекст, простые слова без идиом, термины из `GLOSSARY.md`. Добавить правило пользоваться `GLOSSARY-MAP.md`, если оно появилось по CMx-20. Указать, что переформулировка не отменяет правило «только при новом факте» из справочника ночной смены и не является повторным комментарием без смысла.
Проверка: Файл существует, содержит триггер, три элемента и указатель на справочник комментариев. `git grep -c 'Simplified Technical English' -- docs/runbooks/rephrase.md` больше 0. Код не тронут.
Приёмка: Переформулировка без опоры на `GLOSSARY.md` или без простого языка проваливает карточку. Измерение до выбора зафиксировано нулевым поиском по трём терминам.

Причина: Fix already landed in PR #1757, merge commit 50a6f324fcf953e259c6b50706ff9d0528a1175e; git merge-base --is-ancestor 50a6f324 origin/main exit 0. Runbook docs/runbooks/rephrase.md present in origin/main. No duplicate code written.
Закрыл: ses_cmx22_executor
Время закрытия: 2026-11-22T18:24:11Z
## Подтверждение
- PR #2138 MERGED: 2026-11-22 (squash).


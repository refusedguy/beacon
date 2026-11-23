# PSKX-19. Режим документа в заголовке runbook
Статус: done
Фаза: P8
Тип: doc
Владелец файлов: docs/runbooks/verification.md, AGENTS.md
Зависимости: 
Ссылка: https://raw.githubusercontent.com/cursor/plugins/main/pstack/skills/technical-writing/SKILL.md
Агент: ses_pskx19_executor
Время: 2026-11-22T18:30:44Z
Цель: Определить для документа один режим: tutorial, how-to, reference или explanation, и не смешивать режимы внутри одного файла.
Контекст: Источник: `pstack/skills/technical-writing/SKILL.md:23-31` (One document, one mode). `docs/runbooks/verification.md:1-13` начинается с описанияPropertyName скриптов, продолжается командой запуска и таблицами переменных. Режим документа не объявлен, поэтому справочник и объяснение смешаны в одном файле. baseline: `git grep -i -E 'режим документа|Diataxis' -- docs/runbooks/verification.md | wc -l` = 0 на `origin/main`. Карточка фиксирует приём в дереве beacon, не создаёт новый глобальный скилл и не меняет протокол Watch. Не дублирует PSK-01 (кандидаты), PSK-02 (адверсарии), PSK-03 (TLA Watch), CP-01..05, CM-01..05 или закрытые дубли HD-099/HD-105/HD-109. Риск: низкий.
Шаги: Добавить в начало runbook строку «Режим документа: reference» и вынести объясняющие абзацы в связанный документ режима explanation. В `AGENTS.md` добавить правило: новый документ в `docs/` объявляет режим в первом абзаце и не смешивает инструкции со справочником. Не менять содержание проверок.
Проверка: `git grep -n -E 'режим документа|Diataxis' docs/runbooks/verification.md AGENTS.md` находит объявление режима и правило.
Приёмка: В каждом затронутом документе ровно один объявленный режим. Таблицы и пошаговые инструкции не перемешаны без раздела.

Причина: already_landed
Закрыл: beacon
Время закрытия: 2026-11-22T18:33:51Z
## Подтверждение
- PR #2152 MERGED: 2026-11-22 (squash).

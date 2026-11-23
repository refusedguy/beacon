# CMx-19. Прототип как primary source на отдельной ветке с зафиксированным вердиктом
Статус: done
Фаза: P3
Тип: doc
Владелец файлов: docs/design/prototype-capture.md
Зависимости:
Ссылка: docs/research/comp-matt-skills.md; mattpocock/skills@abeeaf9 skills/engineering/prototype/SKILL.md; skills/engineering/ask-matt/SKILL.md
Агент: ses_cmx19_executor
Время: 2026-11-22T18:21:43Z
Цель: Записать в beacon, как сохранять ответ на дизайнерский вопрос вместо того, чтобы терять его в чате или оставлять прототип в основной ветке.
Контекст: Источник: `skills/engineering/prototype/SKILL.md:12-17` требует сначала выбрать ветку вопроса: логика или state model даёт один HTML-файл с кнопками и пошаговым разбором, вопрос «как должно выглядеть» даёт несколько вариантов на одном маршруте с переключателем. Строки `:19-26` задают шесть правил: явно помечать прототип, запускать одной командой, не сохранять состояние по умолчанию, пропустить полировку, показывать полное состояние после каждого действия и зафиксировать результат. Строка `:26` требует после переноса решения в реальный код закоммитить прототип на отдельную ветку вне main, оставить context pointer на ней в задаче и записать сам вердикт вместе с вопросом. `skills/engineering/ask-matt/SKILL.md:87` называет прототип primary source. baseline по `origin/main` `0ce4ba29`: `git grep -i -c -E 'prototype/|primary source' -- AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/design docs/adrs internal cmd tools scripts` даёт одно совпадение `primary source` в `internal/infra/opencode/client.go:114` про источник данных агента и 0 для `prototype/`. Правила сохранения прототипа в дереве нет.
Шаги: Создать `docs/design/prototype-capture.md`. Записать выбор ветки по типу вопроса и шесть правил прототипа. Описать порядок завершения: решение в реальный код, прототип на ветке `prototype/<name>` без мержа в main, указатель на ветку и вердикт в карточке. Указать, что прототип не заменяет тест на согласованном seam из CMx-05.
Проверка: Файл существует, содержит обе ветки, шесть правил и порядок завершения. `git grep -c 'prototype/' -- docs/design/prototype-capture.md` больше 0; `git grep -c 'вердикт' -- docs/design/prototype-capture.md` больше 0. Код не тронут.
Приёмка: Прототип без зафиксированного вердикта или без отдельной ветки проваливает карточку. Измерение до выбора зафиксировано одним совпадением вне темы и нулём по префиксу ветки.

Причина: Already landed in PR #1755 (docs(design): capture prototype as primary source (CMx-19)); merge commit 482b2dca10213fdb1615461602b797bc2c34aea6 is ancestor of origin/main (git merge-base --is-ancestor exit 0); docs/design/prototype-capture.md already on origin/main and satisfies verification. No duplicate code written.
Закрыл: ses_cmx19_executor
Время закрытия: 2026-11-22T18:22:15Z
## Подтверждение
- PR #2128 MERGED: 2026-11-22 (squash).


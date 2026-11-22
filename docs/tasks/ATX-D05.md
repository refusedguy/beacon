# ATX-D05. Дописать определения терминов и список вердиктов
Статус: done
Фаза: P2
Тип: task
Владелец файлов: GLOSSARY.md, README.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать термины без определения и расхождение вердиктов между документами.
Контекст: Источник: docs/research/docs-consistency-gaps.md, находка ATX-D05. В `GLOSSARY.md` определено 23 термина. `green_blocked` описан внутри статьи Verdict, но в `README.md:153` перечислены вердикты без него. Термины `autoMerge`, `requireApproval`, `waitForMain` и `WIRING` не определены ни в глоссарии, ни в README, хотя встречаются в README и карточках. Карточка BCX-20 частично устарела: в глоссарии `green_blocked` уже есть.
Шаги: Добавить четыре отдельные статьи в глоссарий с полями значения и _Avoid_. Сверить список вердиктов в README со списком из `internal/domain/types.go` и статьи Verdict. Добавить `green_blocked` в список README.
Проверка: CI зелёный. Каждый из четырёх терминов имеет статью. Список вердиктов README совпадает со списком глоссария.
Приёмка: Термин из списка отчёта находится в глоссарии по имени. Расхождений в перечне вердиктов нет.

Итог: Cardindex resolves done: OwnerFiles overlap with HD-117 (already done). Supporting merged commit 0a3745c6bb4d (PR #1607) touches the card OwnerFiles.
Доказательство: Commit 0a3745c6bb4d2d60884b069372664f6cb31ca592 (PR #1607, fix(cli): deepen and test doctor checks (CU-05) (#1607)) touches OwnerFiles GLOSSARY.md, README.md; git merge-base --is-ancestor 0a3745c6bb4d2d60884b069372664f6cb31ca592 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1607 0a3745c6bb4d
Время закрытия: 2026-11-22T11:27:01Z

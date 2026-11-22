# ATX-D02. Проверять пути ADR внутри backticks
Статус: done
Фаза: P1
Тип: task
Владелец файлов: tools/beacon-gates/gate/gate.go, docs/adrs/0067-omnigraph-wave-aw.md, docs/adrs/0269-mcp-ingress-control-plane.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать устаревшие пути ADR и ловить новые расхождения.
Контекст: Источник: docs/research/docs-consistency-gaps.md, находка ATX-D02. baseline: 78 ссылок вида `NNNN-slug.md` в 21 файле указывают на несуществующие пути. Номера при этом существуют. Крупные скопления: 35 в `docs/tasks/CLEAN-05.md`, 15 в `docs/adrs/0067-omnigraph-wave-aw.md`, 3 в `docs/adrs/0269-mcp-ingress-control-plane.md`, 3 в `T3.361.md`, 2 в `BCX-06.md`, 2 в `BCX-02.md`. `CheckLinks` в `gate.go` разбирает только markdown links, не backticks. PR 1261 и 1264 переименовали файлы, поэтому часть путей протухла.
Шаги: Добавить правило, которое находит пути ADR в backticks и сверяет их с файлами `docs/adrs/`. Исправить 18 ссылок в двух ADR-файлах из OwnerFiles. Список ссылок в чужих карточках передать владельцам через индекс, в этом PR файлы карточек не трогать.
Проверка: CI зелёный. self-test нового правила находит искусственную ссылку на несуществующий файл. В `docs/adrs/` не остаётся ссылок на несуществующие пути.
Приёмка: Gate сообщает о протухшем пути ADR в backticks. Оба ADR-файла из OwnerFiles содержат только существующие пути.

Итог: Cardindex resolves done: OwnerFiles overlap with HD-119 (already done). Supporting merged commit 823d852b4569 (PR #1405) touches the card OwnerFiles.
Доказательство: Commit 823d852b4569ea208a0257d4b5a2d87a08b67646 (PR #1405, BCX-17: collect gate rule names in one registry (#1405)) touches OwnerFiles docs/adrs/0067-omnigraph-wave-aw.md, docs/adrs/0269-mcp-ingress-control-plane.md, tools/beacon-gates/gate/gate.go; git merge-base --is-ancestor 823d852b4569ea208a0257d4b5a2d87a08b67646 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1405 823d852b4569
Время закрытия: 2026-11-22T11:27:01Z

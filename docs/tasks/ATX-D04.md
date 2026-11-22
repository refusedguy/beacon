# ATX-D04. Зафиксировать канонические пути docs-артефактов
Статус: done
Фаза: P2
Тип: task
Владелец файлов: README.md, docs/research/docs-consistency-gaps.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Дать один указатель статуса производных документов вместо устаревших путей.
Контекст: Источник: docs/research/docs-consistency-gaps.md, находка ATX-D04. Запрошенные пути `docs/adrs/README.md`, `docs/GLOSSARY.md` и `docs/FACTS.md` отсутствуют в `origin/main`. PR 905 (IDX-11) убрал индекс ADR и FACTS из git, PR 929 и 1169 убрали запись этих файлов из генераторов. `.gitignore:24-26` хранит три пути. Актуальный глоссарий живёт в `GLOSSARY.md` в корне. Карточки BCX-02, BCX-05, BCX-20, HD-088 и строка 81 роадмапа продолжают ссылаться на старые пути.
Шаги: Добавить в `README.md` раздел с тремя путями, их статусом и командой получения. Отметить, что индекс ADR создаётся `tools/roadmap-sync`, а FACTS печатается в stdout. Перенести таблицу путей в отчёт без правки чужих карточек.
Проверка: CI зелёный. Каждый из трёх путей в `README.md` помечен как производный или корневой. Команда получения указана для индекса ADR и FACTS.
Приёмка: Читатель находит актуальный путь глоссария и способ получить индекс без ручного создания файла. Старые файлы не возвращены в git.

Итог: Cardindex resolves done: OwnerFiles overlap with HD-117 (already done). Supporting merged commit 0a3745c6bb4d (PR #1607) touches the card OwnerFiles.
Доказательство: Commit 0a3745c6bb4d2d60884b069372664f6cb31ca592 (PR #1607, fix(cli): deepen and test doctor checks (CU-05) (#1607)) touches OwnerFiles README.md, docs/research/docs-consistency-gaps.md; git merge-base --is-ancestor 0a3745c6bb4d2d60884b069372664f6cb31ca592 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1607 0a3745c6bb4d
Время закрытия: 2026-11-22T11:27:01Z

# ATX-D06. Пересобрать ROADMAP_STATUS по текущему дереву
Статус: done
Фаза: P2
Тип: task
Владелец файлов: docs/ROADMAP_STATUS.md, tools/roadmap-sync/main.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Привести счётчики и OwnerFiles роадмапа к состоянию текущего коммита.
Контекст: Источник: docs/research/docs-consistency-gaps.md, находка ATX-D06. В заголовке указан Baseline SHA `a67afaf02961837b47ae3d7a7acd5b3985bf3be2`, что на 44 коммита позади `a7dcb6f6`. Заголовок обещает 4 реальных todo, таблица реестра содержит 206 строк. В OwnerFiles значатся `docs/FACTS.md`, `docs/AGENT-RULES.md`, `docs/SKILL-MAP.md`, а также старые имена ADR `0189-internal-scheduler.md`, `0190-mcp.md`, `0198-outbox.md`. Год пересчёта в заголовке 2026-11-20.
Шаги: Перегенерировать счётчики и дату по текущему SHA. Сверить каждую строку реестра с карточкой в `docs/tasks/`. Добавить проверку, что число в заголовке равно числу строк реестра и что пути OwnerFiles существуют в дереве.
Проверка: CI зелёный. Повторный прогон generator не меняет файл. Проверка падает на искусственном расхождении счётчика или отсутствующем OwnerFile.
Приёмка: Baseline SHA совпадает с HEAD. Число todo в заголовке равно числу строк реестра. Несуществующих OwnerFiles нет.

Итог: Cardindex resolves done: OwnerFiles overlap with BCX-04 (already done). Supporting merged commit 5a72ede9882c (PR #1622) touches the card OwnerFiles.
Доказательство: Commit 5a72ede9882c5882f989f0f89021ecc228b66ec4 (PR #1622, docs(IDX-11): stop committing derived ROADMAP_STATUS.md (#1622)) touches OwnerFiles docs/ROADMAP_STATUS.md, tools/roadmap-sync/main.go; git merge-base --is-ancestor 5a72ede9882c5882f989f0f89021ecc228b66ec4 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1622 5a72ede9882c
Время закрытия: 2026-11-22T11:27:01Z

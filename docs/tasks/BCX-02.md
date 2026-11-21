# BCX-02. Развести 34 дублированных номера ADR и обновить ссылки в карточках
Статус: done
Фаза: P0
Тип: task
Владелец файлов: docs/adrs/README.md, docs/adrs/0091-checked-stop.md, docs/adrs/0091-mcp.md, docs/tasks/T5.52.md
Зависимости: 
Ссылка: ADR 0091
Агент: exe121
Время: 2026-11-21T17:58:53Z
Цель: Сделать ссылку ADR однозначной: один номер закреплён за одной записью, карточки ссылаются на конкретный файл.
Контекст: Источник: docs/research/docs-health-audit.md, находка 2 и таблица 7 строка 2. 34 номера имеют по два файла, 0185 по три. 0091-checked-stop.md и 0091-mcp.md оба accepted; десять карточек пишут ссылку ADR 0091, включая T5.50, T5.51, T5.52, T3.269. Базовый замер и приоритет из отчёта: P0.
Шаги: Инвентаризировать все номера с несколькими файлами. Назначить уникальные ID втором и третьему решению. Обновить поле Ссылка в десяти карточках. Добавить rule в docsgate на повтор номера.
Проверка: CI зелёный. docsgate не находит дубли номеров. Каждая ссылка ADR 0091 разрешается в один файл.
Приёмка: Ссылка в T5.52 указывает на проверенный останов. Ни один номер ADR не имеют две accepted-записи.

Итог: Already landed in PR #1264 (commit bf4a08ec6e530e02ae35864b9f08412338ff6952): 34 duplicate ADR numbers resolved, ADR 0091-mcp.md renamed to 0276-mcp.md, card links updated
Доказательство: PR #1264 merged 2026-11-21T16:50:23Z; commit bf4a08ec6e530e02ae35864b9f08412338ff6952 changes docs/adrs and tools/beacon-gates/gate/config/docs-baseline.json; git merge-base --is-ancestor bf4a08ec6e530e02ae35864b9f08412338ff6952 origin/main exit 0; no open duplicate PR
Причина: already landed in PR #1264 bf4a08ec
Время закрытия: 2026-11-21T18:04:34Z

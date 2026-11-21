# BCX-24. Не печатать результат при ненулевом коде выхода
Статус: done
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/orchestrate.go, cmd/beacon/workload.go, cmd/beacon/prcreate.go
Зависимости: 
Ссылка: ADR 0016
Агент: exe141
Время: 2026-11-21T18:06:47Z
Цель: Обеспечить пустой stdout при ошибке в orchestrate, workload и pr create.
Контекст: Источник: docs/research/cli-ux-audit.md, таблица 5 строка 4. orchestrate.go:245-258, workload.go:142-156 и prcreate.go:318-341 пишут причину в stderr, но рендерят таблицу или JSON в stdout и возвращают 2. ADR 0016 зафиксирован в docs/adrs/0016-build-system.md:24. Базовый замер и приоритет из отчёта: P1.
Шаги: Рендерить результат только при err == nil. При ошибке оставить stdout пустым и вернуть ненулевой код. Добавить тест, который вызывает команды в ошибочном состоянии и проверяет stdout.
Проверка: CI зелёный. Тесты утверждают пустой stdout при коде 2. Причина ошибки остаётся в stderr.
Приёмка: Потребитель не может принять результат за успех при ненулевом коде.

Итог: Уже влито в PR #1281 (коммит 90b94ce5faf1583f657262ea44d9892228bda6b5): пустой stdout при ошибке в orchestrate, workload и pr create
Доказательство: PR #1281 merged 2026-11-21T17:36:19Z; коммит 90b94ce5faf1583f657262ea44d9892228bda6b5 меняет cmd/beacon/orchestrate.go, cmd/beacon/workload.go, cmd/beacon/prcreate.go; git merge-base --is-ancestor 90b94ce5faf1583f657262ea44d9892228bda6b5 origin/main exit 0; открытого PR-дубля нет
Причина: already landed in PR #1281 90b94ce5
Время закрытия: 2026-11-21T18:07:38Z

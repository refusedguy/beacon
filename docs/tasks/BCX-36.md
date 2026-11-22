# BCX-36. Убрать N+1 и полные чтения в горячих путях store
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/store/db.go, internal/infra/store/archive.go, internal/infra/store/backfill_registry.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Сократить время под единственным writer и число JSON-декодирований на цикл.
Контекст: Источник: docs/research/sqlite-perf-audit.md, топ-10 строка 6. InboxClaim в db.go:857-890 делает SELECT и по UPDATE на сообщение. ArchiveClosedWatches в archive.go:91-135 читает и декодирует все закрытые Watch. ActiveWatches, LiveWatches, AllWatches декодируют payload на каждом вызове. BackfillRegistry в backfill_registry.go:91-165 проходит все Watch. ArchivedWatch вызывает ensureWatchArchive из обычного чтения. Базовый замер и приоритет из отчёта: P1.
Шаги: Заменить пошаговые UPDATE на UPDATE ... WHERE key IN (...). Добавить LIMIT и курсор для backfill. Фильтровать по времени в SQL. Подготовить таблицы один раз при Open, а не из readout. Измерить время под writer до и после.
Проверка: CI зелёный. Тест на наборе из тысячи Watch показывает меньшее число запросов. Чтение архива не захватывает writer.
Приёмка: Число UPDATE в цикле фиксировано и не растёт линейно. Обычное чтение не вызывает DDL.

Итог: Already landed in PR #1303 (commit d900322eeeae399ade7de6baf01894d5e128617f). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1303 merged 2026-11-22T09:31:23+05:00; commit d900322eeeae399ade7de6baf01894d5e128617f; git merge-base --is-ancestor d900322eeeae399ade7de6baf01894d5e128617f origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1303 d900322e
Время закрытия: 2026-11-22T11:24:28Z

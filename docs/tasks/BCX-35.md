# BCX-35. Добавить составные индексы под горячие предикаты
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/store/schedules.go, internal/infra/store/receipts.go, internal/infra/store/blockers.go, internal/infra/store/registry_scoped.go, internal/infra/store/db.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать полные сканирования по шести предикатам на append-only таблицах.
Контекст: Источник: docs/research/sqlite-perf-audit.md, топ-10 строка 5. schedules.go:121-122 ищет enabled = 1 AND next_run <= ? без индекса. receipts.go:561 делает count(*) WHERE outcome = ?. blockers.go:365 сортирует по created_at вне префикса existing индекса. registry_scoped.go:234-236,262-265 фильтрует project_key и watch_id. db.go:860,928,2294 сортирует inbox и queue_tasks по created_at. Базовый замер и приоритет из отчёта: P2.
Шаги: Добавить индексы (enabled, next_run), (outcome), (status, created_at), (project_key, watch_id), (peer_name, state, created_at), (queue, created_at, id). Проверить планы запросов до и после на тестовом наборе. Учесть стоимость записи в индексы.
Проверка: CI зелёный. Планы шести запросов используют новые индексы. Тест миграции на непустой базе проходит.
Приёмка: Ни один из шести предикатов не вызывает полное сканирование растущей таблицы.

Итог: Already landed in PR #1419 (commit 3a08fa9698217a4bf38e6a12c24f487bef97da7d). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1419 merged 2026-11-22T09:09:26+05:00; commit 3a08fa9698217a4bf38e6a12c24f487bef97da7d; git merge-base --is-ancestor 3a08fa9698217a4bf38e6a12c24f487bef97da7d origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1419 3a08fa96
Время закрытия: 2026-11-22T11:24:28Z

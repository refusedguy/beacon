# BCX-34. Включить project_key в PRIMARY KEY watch_bindings
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/store/registry.go, internal/infra/store/watch_bindings.go, internal/infra/store/registry_scoped.go
Зависимости: 
Ссылка: ADR 0086
Агент: нет
Время: нет
Цель: Убрать молчаливую потерю записи при одинаковом watchID и revision в двух проектах.
Контекст: Источник: docs/research/sqlite-perf-audit.md, топ-10 строка 4. registry.go:71-84 и watch_bindings.go:34-48 задают ключ (watch_id, revision) без project_key. Запись через INSERT OR IGNORE, scoped-чтение фильтрует по project_key (registry_scoped.go:185-216). Вторая запись молча отбрасывается. Базовый замер и приоритет из отчёта: P1.
Шаги: Спроектировать миграцию с новым PRIMARY KEY (project_key, watch_id, revision). Проверить уникальность существующих данных до пересборки. Убрать OR IGNORE там, где коллизия должна быть ошибкой. Добавить тест с двумя проектами.
Проверка: CI зелёный. Тест двух проектов с одинаковым watchID сохраняет обе записи. Миграция на реальных данных проходит.
Приёмка: Изоляция проектов по watch_bindings не зависит от порядка записи.

Итог: Already landed in PR #1298 (commit 2e19434cb76768967973901e921c150acc0afdcd). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1298 merged 2026-11-22T09:35:51+05:00; commit 2e19434cb76768967973901e921c150acc0afdcd; git merge-base --is-ancestor 2e19434cb76768967973901e921c150acc0afdcd origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1298 2e19434c
Время закрытия: 2026-11-22T11:24:28Z

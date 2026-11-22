# BCX-37. Сделать запись данных и progress одной транзакцией
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/store/observability.go, internal/infra/store/db.go
Зависимости: 
Ссылка: ADR 0067
Агент: нет
Время: нет
Цель: Убрать вторую транзакцию и CREATE TABLE IF NOT EXISTS на каждой записи.
Контекст: Источник: docs/research/sqlite-perf-audit.md, топ-10 строка 7. observability.go:153-196 на каждую запись тратит две транзакции: данные и persist_progress. Обе выполняют CREATE TABLE IF NOT EXISTS. Progress не атомарен с данными, сбой оставляет счётчик позади. Базовый замер и приоритет из отчёта: P1.
Шаги: Создать таблицы один раз при Open. Перенести обновление progress в транзакцию данных или писать счётчик пачкой. Сверить семантику ack с ADR 0067. Добавить тест на сбой между операциями.
Проверка: CI зелёный. Тест проверяет одну транзакцию на запись. Progress не отстаёт после искусственного сбоя.
Приёмка: Число writer-транзакций на запись уменьшено вдвое. Счётчик не расходится с данными.

Итог: Already landed in PR #1310 (commit 231ad3a8b8c4fcb04e775d51123b67b48092c551). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1310 merged 2026-11-21T22:51:25+05:00; commit 231ad3a8b8c4fcb04e775d51123b67b48092c551; git merge-base --is-ancestor 231ad3a8b8c4fcb04e775d51123b67b48092c551 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1310 231ad3a8
Время закрытия: 2026-11-22T11:24:28Z

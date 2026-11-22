# BCX-30. Добавить машинный формат командам с ад-hoc выводом
Статус: done
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/deliver.go, cmd/beacon/inspect.go, cmd/beacon/human.go, cmd/beacon/workload.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Отдавать структурированный результат там, где данные уже размечены JSON-тегами.
Контекст: Источник: docs/research/cli-ux-audit.md, таблица 5 строка 10. deliverResult в deliver.go:298-306 имеет JSON-теги, но runDeliver печатает res.line(). human.go:263-272, registry_stop.go:268-272, inspect.go:187-217 и workload.go:160-169 печатают строки. inspect --json на пустом состоянии даёт blockers: null вместо []. Базовый замер и приоритет из отчёта: P2.
Шаги: Добавить --format json и кодировать структуру в deliver, human, inspect, doctor, workload. В inspect превращать nil в пустой массив по образцу agents.go:334-336 и registry_list.go:93-95.
Проверка: CI зелёный. Тесты кодирования для всех пяти команд. inspect --json на пустом состоянии даёт blockers: [].
Приёмка: Потребитель получает стабильный JSON без дополнительной ветки для null.

Итог: Already landed in PR #1305 (commit 99e7e193684ffcb4e280bacb488348e901b649c8). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1305 merged 2026-11-22T08:58:22+05:00; commit 99e7e193684ffcb4e280bacb488348e901b649c8; git merge-base --is-ancestor 99e7e193684ffcb4e280bacb488348e901b649c8 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1305 99e7e193
Время закрытия: 2026-11-22T11:24:28Z

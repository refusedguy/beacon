# BCX-61. Добавить живые тесты Board TUI, GC и shutdown
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/tui/app.go, internal/infra/server/lifecycle_test.go, docs/tasks/S7.011.md
Зависимости: 
Ссылка: ADR 0061
Агент: нет
Время: нет
Цель: Закрыть три пробела: TUI против сервера, kill на середине wait_result и старый PID.
Контекст: Источник: docs/research/e2e-coverage-audit.md, таблица строка 10. Есть unit-тесты TUI с фейковым сокетом, lifecycle и watch GC, спецификации U01-U04 и E10. Нет прогона TUI против сервера, kill на середине wait_result и проверки старого PID. crash_recovery_durable_store_no_old_pid из ADR 0061 пункт 5 не написан. Базовый замер и приоритет из отчёта: P2.
Шаги: Поднять сервер и подключить TUI к живому сокету. Измерить первый кадр и RSS. Убить сервис во время wait_result и проверить, что старый PID и lease не читаются. Добавить сценарий crash recovery по ADR 0061.
Проверка: CI зелёный. Три живых сценария проходят. Замеры первого кадра и RSS записаны в карточку или отчёт.
Приёмка: TUI, GC и shutdown подтверждены на живом сервере, а не на фейковом сокете.

Итог: Already landed in PR #1312 (commit 7089eae5d5d8d014b9888cdb41cfda2d043371f9). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1312 merged 2026-11-22T09:00:56+05:00; commit 7089eae5d5d8d014b9888cdb41cfda2d043371f9; git merge-base --is-ancestor 7089eae5d5d8d014b9888cdb41cfda2d043371f9 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1312 7089eae5
Время закрытия: 2026-11-22T11:24:28Z

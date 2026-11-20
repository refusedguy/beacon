# BS.76. Eval E03 защиты от loop
Статус: done
Фаза: P6
Тип: eval
Владелец файлов: docs/evals/background-server.md
Зависимости: BS.19, BS.65
Ссылка: ADR 0199
Агент: нет
Время: нет
Цель: Завершённая задача не просыпается сама.
Контекст: blockedNotified, terminal guard и snooze работают вместе.
Шаги: Seed late event, completion wake и pending question.
Проверка: Один блок даёт один steer; терминальный статус не меняется.
Приёмка: Негативный контроль: без guard'ов агент будится в цикле.

## Подтверждение
- PR #391 MERGED: 2026-11-20 (squash).

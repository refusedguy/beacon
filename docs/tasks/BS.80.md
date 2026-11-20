# BS.80. Runbook и документация сервера
Статус: dropped
Фаза: P6
Тип: task
Владелец файлов: docs/runbooks/background-server.md, docs/adrs/README.md
Зависимости: BS.3, BS.45, BS.72
Ссылка: ADR 0188
Агент: нет
Время: нет
Цель: Оператор может запустить и диагностировать сервер.
Контекст: openrig и t3code держат runbook рядом с архитектурой.
Шаги: Запуск, стоп, health, метрики, логи, восстановление, безопасность.
Проверка: По runbook сервер поднимается на чистой машине.
Приёмка: Числа в документации сверены docs guard.

## Подтверждение
- PR #394 DROPPED: Runbook for the removed background-server; current ops docs are docs/runbooks/observability.md and verification.md; background-server.md is obsolete.

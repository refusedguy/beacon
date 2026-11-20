# BS.14. Пул EffectWorker с lease и heartbeat
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/app/worker/effect.go, internal/infra/store/outbox.go
Зависимости: BS.12, BS.7
Ссылка: ADR 0191
Агент: нет
Время: нет
Цель: Фоновые эффекты без процессов на задачу.
Контекст: 15 процессов gh давали 668.8 MB RSS и 5400 запросов в час.
Шаги: Один CI-клиент, воркеры по типам, lease_until, last_heartbeat, attempts.
Проверка: При росте вотчей число процессов gh не растёт.
Приёмка: Retry с backoff и потолком попыток.

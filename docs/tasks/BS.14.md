# BS.14. Пул EffectWorker с lease и heartbeat
Статус: done
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
Premise проверен 2026-11-20, дубль не создан. Функциональность уже в main: коммит f4c870f (PR #468) добавил `internal/app/worker/effect.go` с `EffectWorker` (`New`, `Drain`, `Completed`) и идемпотентным `EffectRunner`; `Drain` прогоняет эффекты по одному, хранит выполненные ID в `executed` и возвращает ошибки раннера в `DrainReport` без отката коммита. Часть про lease и heartbeat закрыта отдельной карточкой BS.15 (commit e56761b, `PlanRetire`/`Retirer` в `internal/app/worker/retire.go`), тоже уже в main. Тесты `TestDrainRunsEffectOnce`, `TestDrainRetryDoesNotRepeatWork` и `TestDrainContinuesAfterFailure` в `effect_test.go` покрывают однократный запуск, ретрай без повтора работы и продолжение после ошибки. Статус done карточка получила в коммите 5574278 (PR #533). Код в OwnerFiles не писал.

# BS.33. Персистентность расписаний
Статус: done
Premise: проверено 2026-11-20, дубль. Функциональность уже в main: internal/infra/store/schedules.go хранит расписания в таблице schedules с колонками next_run и enabled, next_run в Unix-наносекундах. SaveSchedule пишет или обновляет строку, DueSchedules выдаёт просроченные расписания, MarkTriggered сдвигает next_run вперёд, поэтому просрочка выполняется один раз за период. Тесты TestSchedulesPersistAcrossReopen и TestMarkTriggeredShiftsNextRun в internal/infra/store/schedules_test.go покрывают проверку и приёмку карточки. Код не писал, OwnerFiles не менял, дубль не создан.
Фаза: P2
Тип: task
Владелец файлов: internal/infra/store/schedules.go
Зависимости: BS.23, BS.5
Ссылка: ADR 0192
Агент: нет
Время: нет
Цель: Расписания переживают рестарт.
Контекст: Состояние сервера должно быть переживающим рестарт.
Шаги: Таблица schedules, next_run_at, enabled, last_run, версия конфига.
Проверка: После рестарта next_run_at сохраняется.
Приёмка: Просроченный next_run_at выполняется один раз.

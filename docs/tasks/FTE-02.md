# FTE-02. Подключить SQLite-расписания к task CLI
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/task.go, cmd/beacon/task_test.go
Зависимости:
Ссылка: docs/tasks/INTG-03.md:12, docs/evals/intg-control-plane-integrity.md:10-12, docs/research/research-feature-gaps.md:12,43
Агент: нет
Время: нет
baseline: cmd/beacon/task.go:902-903 вызывает server.NewFileScheduleStore. Внутри internal/infra/store/schedules.go:287 определены CreateSchedule, GetSchedule, ListSchedules, UpdateSchedule, DeleteSchedule и SetScheduleEnabled. Production-вызовов этих методов нет, 0 точек.
Цель: beacon task schedule пишет расписания в таблицу schedules через store из INTG-03, а не в schedules.json.
Контекст: INTG-03 закрыта и добавила CRUD в internal/infra/store/schedules.go. eval intg-control-plane-integrity:10-12 требует, чтобы расписание, записанное через Create, читалось через Get и присутствовало в List, а повторный Create возвращал ошибку. Адаптер остался за пределами OwnerFiles INTG-03. cmd/beacon/task.go:902 по-прежнему создаёт файловый store.
Шаги: Реализовать адаптер server.ScheduleStore поверх *store.DB. Заменить scheduleStoreFor. Покрыть тестом идемпотентности и чтения после повторного открытия store. Оставить файловый бэкенд только в тестах.
Проверка: beacon task schedule create создаёт запись, видимую через store.ListSchedules. Файл schedules.json не создаётся. Повторное создание возвращает ту же ошибку, что и файловый store. Удалённое расписание пропадает из List.
Приёмка: Файловый бэкенд больше не используется в production, CI зелёный.

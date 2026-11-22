# INTG-03. Добавить CRUD расписаний в SQLite и убрать schedules.json
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/store/schedules.go, internal/infra/store/schedules_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Сделать таблицу schedules полной реализацией порта server.ScheduleStore и устранить двойное хранение расписаний.
Контекст: Источник: docs/research/audit-05-integrity.md, раздел "Новое, что не вошло в AUD", пункт 3. internal/infra/server/scheduler.go:64 заявляет, что BS.33 реализует порт поверх таблицы. Фактической реализации нет. cmd/beacon/task.go:828-829 вызывает NewFileScheduleStore с путём schedules.json, а комментарий в :825 говорит, что порт пока реализован файлом. internal/infra/store/schedules.go:73,111,150,184 содержит SaveSchedule, DueSchedules, MarkTriggered и DisableSchedule, но не предоставляет методы Create, Get, List, Update и Delete, которые требуются порту. baseline: 2 storage backends, 0 adapters over the schedules table.
Шаги: Добавить в internal/infra/store/schedules.go методы Create, Get, List, Update, Delete и SetEnabled с теми же правилами идемпотентности и ошибок, что у FileScheduleStore. Покрыть методы тестами в schedules_test.go, включая отсутствующую запись, повторное создание и выключение. Адаптер в cmd/beacon и замена scheduleStoreFor в cmd/beacon/task.go лежат вне OwnerFiles этой карточки и оформляются отдельной wiring-карточкой через beacon_defer_wiring.
Проверка: CI зелёный. Тесты CRUD проходят на открытом и повторно открытом store. Методы возвращают ErrScheduleNotFound для неизвестного ID.
Приёмка: Изменения только в OwnerFiles карточки. internal/infra/store не импортирует internal/infra/server. Таблица schedules содержит все операции порта ScheduleStore.

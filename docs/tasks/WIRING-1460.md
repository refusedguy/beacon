# WIRING-1460. Подключить store-адаптер расписаний в cmd/beacon
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/task.go, cmd/beacon/task_test.go
OwnerFiles: см. поле «Владелец файлов»
Зависимости: PR 1460
Ссылка: PR 1460
SourceWatch: watch исходного PR #1460 закрыт после мержа, headSHA f9ea57849efeb9564da0b435f84d982d0e82452c
PR: 1460
Агент: нет
Время: нет
Цель: Подключить адаптер к порту server.ScheduleStore и переключатель scheduleStoreFor в cmd/beacon/task.go.
Контекст: PR #1460 добавил CRUD расписаний в internal/infra/store. Адаптер к порту server.ScheduleStore и перевод store-ошибок в ошибки порта остались за пределами OwnerFiles. Переключатель scheduleStoreFor по-прежнему не выбирает новую реализацию.
Шаги: Дождаться мержа PR 1460; добавить адаптер и переключатель scheduleStoreFor в cmd/beacon/task.go; покрыть адаптер тестами в cmd/beacon/task_test.go; вызвать `beacon deferred done` с watchID из SourceWatch.
Проверка: Переключатель выбирает store-реализацию расписаний, ошибки ErrScheduleExists и ErrScheduleNotFound переводятся в ошибки порта, тесты cmd/beacon зелёные в CI, запись WIRING-1460 переведена в done.
Приёмка: Проводка применена, запись в `beacon deferred --format json` имеет `status: done`, CI зелёный.

## Причина

OwnerFiles PR #1460 ограничены internal/infra/store/schedules.go и его тестом. internal/infra/store не импортирует internal/infra/server, поэтому адаптер к порту и перевод store-ошибок в ошибки порта не помещались в тот PR.

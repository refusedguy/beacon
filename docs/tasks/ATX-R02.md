# ATX-R02. Убрать глобалы и два таймера из TestW8BLiveReadsHTTPProjectionOnEvents
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/watch_w8b_test.go, cmd/beacon/watch.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Передать зависимости живого просмотра в `runWatch` явно и убрать из теста два фиксированных ожидания.
Контекст: Источник: docs/research/flaky-tests-audit.md, разделы 2.2 и 2.4, и docs/research/flaky-tests-remaining.md, строка таблицы 2. Тест в `cmd/beacon/watch_w8b_test.go:132-142` подменяет пять переменных пакета: `watchHTTPStateDir`, `watchHTTPClient`, `watchLiveRunner`, `watchLiveContext` и `watchReconnectDelay`. В `:146-161` он запускает `runWatch` в горутине и трижды ждёт рендер по `time.After(2s)`, затем ещё раз ждёт остановку. `runWatch` определён в `cmd/beacon/watch.go:212`. baseline: пять globals, шесть секунд фиксированного ожидания на три рендера.
Шаги: Собрать зависимости в структуру и передать её в `runWatch` аргументом. Сохранить прежнюю точку подмены для остальных вызовов, если они ещё есть. В тесте создать один контекст с таймаутом из `t.Context()` и ждать рендеры и канал `done` через него. Убрать `time.After(2 * time.Second)` из теста.
Проверка: CI зелёный. В `watch_w8b_test.go` нет подмены пяти переменных пакета и нет `time.After(2 * time.Second)`. Тест проходит при медленном планировщике и не оставляет изменённые globals.
Приёмка: Состояние просмотра не живёт в переменных пакета. Время ожидания управляется контекстом теста.

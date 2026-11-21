# ATX-04. Внедрить источник тиков в TestRunDaemonHeartbeatTicksAndStops
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/daemon_touch_test.go, cmd/beacon/daemon.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Заменить два временных окна в тесте heartbeat на явную синхронизацию.
Контекст: Источник: docs/research/flaky-tests-audit.md, раздел 2.1 и строка таблицы 152. cmd/beacon/daemon_touch_test.go:71, :80, :87 и :93 опрашивает heartbeatRecorder.snapshot раз в мс, затем спит 20 мс и проверяет отсутствие тиков. Проверка «после отмены тиков нет» зависит от того, что 20 мс достаточно. runDaemonHeartbeat описан в cmd/beacon/daemon.go:154.
Шаги: Заменить параметр interval на ticks <-chan time.Time. Продакшен создаёт time.NewTicker, тест посылает два тика вручную. Проверку отсутствия тика сделать через select с default после подтверждения выхода горутины через done. Убрать time.Sleep(20ms).
Проверка: CI зелёный. В тесте нет time.Sleep и нет опроса snapshot с интервалом 1 мс.
Приёмка: Тест управляет тиками вручную. Отсутствие тика проверяется после выхода горутины.

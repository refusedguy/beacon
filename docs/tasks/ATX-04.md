# ATX-04. Внедрить источник тиков в TestRunDaemonHeartbeatTicksAndStops
Статус: done
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

Итог: Already landed in PR #1211 (commit ecf63c3a79406684d85adae3e9cd6fac55e86f6b): ATX-04: deterministic daemon heartbeat ticks
Доказательство: PR #1211; commit ecf63c3a79406684d85adae3e9cd6fac55e86f6b; git merge-base --is-ancestor ecf63c3a79406684d85adae3e9cd6fac55e86f6b origin/main exit 0; TestRunDaemonHeartbeatTicksAndStops drives ticks through a channel of time.Time, sends two ticks manually and has no time.Sleep in the file.
Причина: already landed in PR #1211 ecf63c3a7940
Время закрытия: 2026-11-22T11:47:55Z

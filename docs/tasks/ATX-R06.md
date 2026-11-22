# ATX-R06. Внедрить тики в RunNotifyLoop вместо опроса счётчика
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/app/orchestrator/reminders_loop_test.go, internal/app/orchestrator/reminders_loop.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать опрос каждые 5 мс из теста цикла напоминаний.
Контекст: Источник: docs/research/flaky-tests-audit.md, разделы 2.1 и 5.3, и docs/research/flaky-tests-remaining.md, строка таблицы 6. `TestRunNotifyLoopTicksAndStops` в `internal/app/orchestrator/reminders_loop_test.go:10-48` запускает `RunNotifyLoop` в горутине с интервалом 1 мс, затем опрашивает `calls` каждые 5 мс до дедлайта 2 с и ещё 1 с ждёт выхода по `finished`. `RunNotifyLoop` определён в `internal/app/orchestrator/reminders_loop.go:27`. Это тот же класс, что ATX-04 уже исправляет для daemon heartbeat. baseline: интервал 1 мс, опрос 5 мс, дедлайн 2 с, таймер остановки 1 с.
Шаги: Добавить в `RunNotifyLoop` приём канала тиков. Продакшн создаёт `time.NewTicker(interval)`, тест посылает два тика вручную и читает два вызова `notify` из буферизованного канала. Проверку остановки оставить через канал `done` с таймаутом из `t.Context()`. Убрать мьютекс, счётчик и `time.After(5 * time.Millisecond)` из теста.
Проверка: CI зелёный. В `reminders_loop_test.go` нет `time.After(5 * time.Millisecond)` и нет цикла опроса `calls`. Тест проходит при любой загрузке машины.
Приёмка: Тики приходят по явному каналу. Тест не зависит от того, сколько раз планировщик успел запустить горутину за 2 с.

Итог: Already landed in PR #1637 (commit a5e0a752b84a09a6c2486a8887afb219498ae792): test(orchestrator): drive RunNotifyLoop with ticks instead of polling (ATX-R06)
Доказательство: PR #1637; commit a5e0a752b84a09a6c2486a8887afb219498ae792; git merge-base --is-ancestor a5e0a752b84a09a6c2486a8887afb219498ae792 origin/main exit 0; reminders_loop_test.go drives RunNotifyLoop through a buffered channel of time.Time, waits on a context.WithTimeout from t.Context and has no time.After(5ms) polling loop.
Причина: already landed in PR #1637 a5e0a752b84a
Время закрытия: 2026-11-22T11:53:03Z

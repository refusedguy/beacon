# ATX-R04. Согласовать дедлайты в TestBackgroundTaskWaitUnblocksOnComplete
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/tool_get_result_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать два реальных таймера из теста, который уже работает на фейковых часах.
Контекст: Источник: docs/research/flaky-tests-audit.md, раздел 1 и раздел 2.2, и docs/research/flaky-tests-remaining.md, строка таблицы 4. `TestBackgroundTaskWaitUnblocksOnComplete` в `internal/infra/mcp/tool_get_result_test.go:1081-1106` создаёт `testTaskClock` и запускает `task.Wait` с лимитом 5 с в горутине. Дальше тест ждёт `clock.afterCalled` через `time.After(time.Second)` в `:1092` и результат через ещё один `time.After(time.Second)` в `:1104`. Фейковое время и реальные дедлайты не согласованы. baseline: фейковые 5 с против двух реальных таймеров по 1 с.
Шаги: Создать один контекст с таймаутом из `t.Context()` и использовать его для обоих ожиданий. Дождаться регистрации таймера через канал `afterCalled`, продвинуть фейковые часы, вызвать `Complete`, затем прочитать результат из `resCh`. Убрать оба `time.After(time.Second)`.
Проверка: CI зелёный. В тесте остался один источник дедлайта. Проверка `WaitedMs == 40` сохраняется.
Приёмка: Тест не зависит от двух независимых реальных таймеров. Лимит ожидания задаётся контекстом теста.

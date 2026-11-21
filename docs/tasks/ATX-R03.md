# ATX-R03. Убрать опрос SSE по настенным часам в TestEventsHeartbeat
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/httpapi/server_test.go, internal/infra/httpapi/server.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Ждать heartbeat по контексту, а не по циклу с `time.Now()`.
Контекст: Источник: docs/research/flaky-tests-audit.md, раздел 2.1, и docs/research/flaky-tests-remaining.md, строка таблицы 3. `TestEventsHeartbeat` в `internal/infra/httpapi/server_test.go:268-290` подменяет `heartbeatInterval` с 15 с на 20 мс, затем читает `reader.ReadString` в цикле, пока `time.Now().Before(deadline)` с дедлайтом 2 с. Переменная `heartbeatInterval` определена в `internal/infra/httpapi/server.go:68`, тикер создаётся в `:920`. Чтение не привязано к контексту запроса, поэтому зависший вызов не прерывается по таймауту теста. baseline: интервал 20 мс и дедлайн 2 с по настенным часам.
Шаги: Добавить в `server.go` точку подачи тика heartbeat через канал, по которой тест может вызвать кадр без реального ожидания. Передать контекст с таймаутом из `t.Context()` в чтение SSE. Восстанавливать `heartbeatInterval` через `t.Cleanup`, а не через `defer`. Убрать цикл с `time.Now().Add(2 * time.Second)`.
Проверка: CI зелёный. В `TestEventsHeartbeat` нет `time.Now()` и нет чтения без контекста. Кадр heartbeat появляется после явного тика.
Приёмка: Тест завершается по контексту и не зависит от скорости планировщика. Глобальный интервал всегда возвращается к прежнему значению.

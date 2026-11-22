# ATX-R08. Объединить ожидания в TestReceiverAcknowledgesBeforeRecording
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/webhook/receiver_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Заменить два независимых таймера одним контекстом и убрать отрицательные проверки по времени.
Контекст: Источник: docs/research/flaky-tests-audit.md, раздел 1, и docs/research/flaky-tests-remaining.md, строка таблицы 8. `TestReceiverAcknowledgesBeforeRecording` в `internal/infra/webhook/receiver_test.go:247-273` запускает `ServeHTTP` в горутине, ждёт код ответа через `time.After(2 * time.Second)` в `:264`, затем ждёт запуск фонового рекордера через второй такой же таймер в `:269`. Дополнительно в `:135` и `:347` отрицательные проверки используют окна по 100 мс. baseline: два таймера по 2 с и два окна по 100 мс.
Шаги: Создать один `context.WithTimeout` из `t.Context()` и использовать его для обоих ожиданий. Дождаться записи в `block.started`, затем закрыть `block.release` и дождаться завершения горутины через `done`. Отрицательные проверки перестроить на подтверждённом вызове рекордера, а не на истечении 100 мс. Убрать `time.After` из теста.
Проверка: CI зелёный. В тесте нет двух `time.After(2 * time.Second)` и нет `time.After(100 * time.Millisecond)`. Порядок ответа и записи сохраняется.
Приёмка: Оба ожидания управляются одним дедлайтом. Отрицательные утверждения не зависят от скорости планировщика.

Итог: Cardindex resolves done: OwnerFiles overlap with RS-WK-04 (already done). Supporting merged commit c1ad8d49b50c (PR #874) touches the card OwnerFiles.
Доказательство: Commit c1ad8d49b50cf6da58e4e3aa82271e38c4643e27 (PR #874, Distinguish duplicate and conflicting webhook deliveries (#874)) touches OwnerFiles internal/infra/webhook/receiver_test.go; git merge-base --is-ancestor c1ad8d49b50cf6da58e4e3aa82271e38c4643e27 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #874 c1ad8d49b50c
Время закрытия: 2026-11-22T11:27:01Z

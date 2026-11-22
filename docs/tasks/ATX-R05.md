# ATX-R05. Заменить тишину по default на ожидание выхода слушателя doorbell
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/tool_wake_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Сделать отрицательную проверку doorbell детерминированной.
Контекст: Источник: docs/research/flaky-tests-audit.md, раздел 2.2, и docs/research/flaky-tests-remaining.md, строка таблицы 5. `awaitDoorbell` в `internal/infra/mcp/tool_wake_test.go:2957-2966` ждёт результат 3 с. `assertDoorbellQuiet` в `:2968-2975` проверяет отсутствие сообщения через `select` с `default`. Такая проверка верна только в момент чтения: сообщение может прийти через миллисекунду. `startDoorbellListener` в `:2948-2955` уже возвращает канал, а `Doorbell.Listen` в `internal/infra/mcp/tool_wake.go:1595` принимает контекст. baseline: фиксированные 3 с и проверка `default` без синхронизации с горутиной.
Шаги: Передавать контекст в `startDoorbellListener`. Для отрицательных случаев отменять контекст, ждать записи в канал `listenResult` и требовать `context.Canceled`. Убрать `assertDoorbellQuiet` с веткой `default`. Таймаут ожидания взять из `t.Context()`.
Проверка: CI зелёный. В файле нет `assertDoorbellQuiet` с `default` и нет `time.After(3 * time.Second)`. Отрицательные тесты получают ошибку отмены от самого слушателя.
Приёмка: Отсутствие wake подтверждается завершением слушателя с `context.Canceled`, а не пустым каналом в один момент времени.

Итог: Cardindex resolves done: OwnerFiles overlap with T3.239 (already done). Supporting merged commit 1f3f55bfa949 (PR #1188) touches the card OwnerFiles.
Доказательство: Commit 1f3f55bfa9497fe8b35cfacde9e47e50dafc7eb1 (PR #1188, refactor(clean14): remove unwired wake constructors (#1188)) touches OwnerFiles internal/infra/mcp/tool_wake_test.go; git merge-base --is-ancestor 1f3f55bfa9497fe8b35cfacde9e47e50dafc7eb1 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1188 1f3f55bfa949
Время закрытия: 2026-11-22T11:27:01Z

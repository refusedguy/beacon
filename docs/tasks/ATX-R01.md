# ATX-R01. Убрать фиксированные таймауты из тестов verdict source
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/verdict_source_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Заменить четыре независимых таймера в тестах подписки на вердикты одним ожиданием с контекстом.
Контекст: Источник: docs/research/flaky-tests-audit.md, раздел 2.2, и docs/research/flaky-tests-remaining.md, строка таблицы 1. `recvVerdict` в `internal/infra/mcp/verdict_source_test.go:52` ждёт 2 с, `assertNoVerdict` в `:63` проверяет тишину за 60 мс, `:160` ждёт 2 с завершения `pollOnce`, `:248` ждёт 2 с вызова `Stop`. Проверка за 60 мс не доказывает отсутствие сообщения: оно может прийти сразу после проверки. `DBVerdictSource.Stop` в `internal/infra/mcp/verdict_source.go:77` уже дожидается канала `done`. baseline: 2 с, 60 мс, 2 с и 2 с в одном файле.
Шаги: Завести один хелпер ожидания, который берёт дедлайн из `t.Context()`. Перевести на него четыре точки ожидания. В `assertNoVerdict` закрыть подписку, вызвать `Stop`, дождаться выхода горутины и после этого проверить, что канал обновлений пуст. Убрать проверку тишины по истечении 60 мс.
Проверка: CI зелёный. В файле нет `time.After(60 * time.Millisecond)` и четырёх вызовов `time.After(2 * time.Second)`. Отрицательный тест завершается после подтверждённого выхода горутины.
Приёмка: Время ожидания задаётся контекстом теста. Отрицательная проверка не зависит от того, успеет ли сообщение прийти через миллисекунду после неё.

Итог: Already landed in PR #1594 (commit f53bacb50c03fd1df8b68c5c28d4459976db498f): fix(taskstore): recognize new card IDs in RebuildDir (ATX-R01, TUI-07A) (#1594)
Доказательство: PR #1594; commit f53bacb50c03fd1df8b68c5c28d4459976db498f; git merge-base --is-ancestor f53bacb50c03fd1df8b68c5c28d4459976db498f origin/main exit 0; no duplicate code PR needed.
Причина: already landed in PR #1594 f53bacb50c03
Время закрытия: 2026-11-22T11:27:01Z

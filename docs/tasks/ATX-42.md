# ATX-42. Экспортировать wake_duplicate_total
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/observe/metrics.go, internal/infra/observe/observe.go, internal/infra/observe/coverage_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Сделать счётчик повторных доставок видимым во всех форматах.
Контекст: Источник: docs/research/metrics-audit.md, таблица топ-10 и раздел «Почему wake_duplicate_total не появится в экспорте». internal/infra/observe/observe.go:101 объявляет CounterWakeDuplicate, но metrics.go:19-32 не включает его в counterLabels. Metrics.Inc в :194 пишет в m.values, а JSON в :306 и Prometheus в :434 перебирают CounterNames(). Счётчик инкрементируется в wake/deliver.go:122 и :146, но не попадает ни в JSON, ни в Prometheus, ни в снимок. TestEveryCounterIsExportedAndIncrements в coverage_test.go:12 перебирает тот же список, жёсткий want в :39-45 тоже не содержит имя. Eval docs/evals/r02-same-pointer-once.md:11 требует роста счётчика. Baseline: 0 экспортированных серий при ненулевом инкременте.
Шаги: Добавить wake_duplicate_total в counterLabels и в список want coverage_test.go. Добавить проверку, что инкремент виден в JSON и Prometheus. Обновить docs/runbooks/observability.md:57-74.
Проверка: CI зелёный. Тест инкрементирует WakeDuplicate и видит имя в JSON(), Prometheus() и SnapshotJSON().
Приёмка: Счётчик экспортируется во всех трёх форматах. Eval R02 может проверить его рост.

Итог: Already landed in PR #1381 (commit 80d3b0cecdb2fbb75199bc103486305210cc66c7): feat(observe): export wake_duplicate_total in all formats (ATX-42) (#1381)
Доказательство: PR #1381; commit 80d3b0cecdb2fbb75199bc103486305210cc66c7; git merge-base --is-ancestor 80d3b0cecdb2fbb75199bc103486305210cc66c7 origin/main exit 0; no duplicate code PR needed.
Причина: already landed in PR #1381 80d3b0cecdb2
Время закрытия: 2026-11-22T11:27:01Z

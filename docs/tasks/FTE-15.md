# FTE-15. Добавить гистограмму retry_open_duration_seconds
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/observe/metrics.go, internal/infra/observe/observe.go, internal/infra/observe/metrics_test.go
Зависимости: FTE-14
Ссылка: docs/evals/e08-metrics-stay-low-cardinality.md:894-902, docs/runbooks/observability.md:450-452
Агент: ses_fte15c_executor
Время: 2026-11-22T18:16:00Z
baseline: git grep retry_open и RetryOpen по internal, cmd и observe даёт 0 совпадений. В internal/infra/observe/metrics.go зарегистрированы четыре гистограммы: poll_cycle_duration_seconds, merge_duration_seconds, wake_duration_seconds и run_duration_seconds.
Цель: Время, которое обязательство ждёт повтора после неудачной доставки, видно как гистограмма в Prometheus.
Контекст: docs/evals/e08-metrics-stay-low-cardinality.md:898 требует retry_open_duration_seconds с корзинами и sum. docs/runbooks/observability.md:450-452 описывает формат гистограмм с наборами _bucket{le=...}, _sum и _count. Сейчас время повтора измеряется только в логах лестницы, метрики нет.
Шаги: Зарегистрировать гистограмму retry_open_duration_seconds без высококардинальных меток. Записывать длительность между неудачной доставкой и следующим rung лестницы. Ограничиться существующим набором меток observe. Добавить тест на presence корзин, sum и count. Не менять формат существующих гистограмм.
Проверка: beacon metrics --format prometheus содержит retry_open_duration_seconds_bucket{le=...}, _sum и _count. После одного повтора count равен единице и sum больше нуля. Меток watchID и sessionID нет.
Приёмка: Eval e08 проходит, CI зелёный.

Причина: Уже влито в main через PR #1943 (SHA 17269244 feat(observe): add retry_open_duration_seconds histogram (FTE-15)); git merge-base --is-ancestor 17269244 origin/main -> exit 0. Гистограмма retry_open_duration_seconds зарегистрирована в internal/infra/observe/metrics.go (HistogramNames) и записывается в observe.go (RetryOpen); тест корзин/sum/count есть в metrics_test.go. Дублирующий код PR не открывается.
Закрыл: ses_fte15c_executor
Время закрытия: 2026-11-22T18:16:25Z
# FTE-14. Добавить счётчик provider_call_total
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/observe/observe.go, internal/infra/observe/metrics.go, internal/infra/observe/coverage_test.go
Зависимости:
Ссылка: docs/evals/e08-metrics-stay-low-cardinality.md:894-902, docs/runbooks/observability.md:371
Агент: нет
Время: нет
baseline: observe.ProviderCall в internal/infra/observe/observe.go:805 пишет только структурированную строку provider_call. git grep не находит provider_call_total в метриках, 0 совпадений.
Цель: Вызовы CI-провайдера видны как счётчик с метками adapter, method и outcome рядом с логом provider_call.
Контекст: docs/evals/e08-metrics-stay-low-cardinality.md:897 требует provider_call_total с метками adapter, method и outcome. Строка 902 объявляет взрыв серий при появлении метки sessionID. docs/runbooks/observability.md:371 описывает provider_call только как отладочное событие уровня debug, счётчика в таблице метрик нет. Точки вызова уже есть в internal/infra/cibridge/ci_refresh.go:53 и :70.
Шаги: Зарегистрировать счётчик provider_call_total с фиксированным набором меток adapter, method и outcome. Инкрементировать его в ProviderCall. Не добавлять watchID или sessionID в метки. Обновить coverage_test и прометеевский рендер. Сохранить существующую строку лога.
Проверка: beacon metrics --format prometheus содержит provider_call_total с тремя метками. После одного вызова batchci FetchBatch значение равно единице. Меток sessionID и watchID нет ни в одной метрике.
Приёмка: Eval e08 проходит, CI зелёный.

# Q-B3. Восстановление dedupe индекса и Conflict против Duplicate
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/app/worker/effect.go, internal/infra/webhook/receiver.go, internal/infra/webhook/filters.go, internal/infra/webhook/workflow_run.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Индекс восстанавливается из журнала, повтор и конфликт.params различаются.
Контекст: Premise: u9 и u10 влили PR #872 и #874. Требуется свести и проверить, что Conflict не превращается в Duplicate.
Шаги: Проверить оба change в main, добавить интеграционный тест на рестарт плюс изменение тела, закрыть RS-WK-03 и RS-WK-04.
Проверка: gofmt, go test в CI.
Приёмка: Один эффект на доставку, конфликт тела логируется отдельно.

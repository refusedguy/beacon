# BS.13. Receipt store по clientRequestId
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/store/receipts.go
Зависимости: BS.12
Ссылка: ADR 0196
Агент: нет
Время: нет
Цель: Повтор команды возвращает прежний результат.
Контекст: T3 держит receipts дольше журнала для идемпотентности.
Шаги: UNIQUE(client_request_id), receipt_json, intent_committed и effect_status отдельно.
Проверка: Два одинаковых вызова создают один command.
Приёмка: Receipt не означает завершение эффекта.

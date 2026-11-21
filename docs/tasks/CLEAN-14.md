# CLEAN-14. Удалить невызываемые wake-символы по ADR 0263
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/tool_wake.go, internal/infra/mcp/tool_wake_test.go, internal/infra/wake/deliver.go, internal/infra/wake/deliver_test.go
Зависимости: 
Ссылка: ADR 0263
Агент: нет
Время: нет
Цель: Убрать AttachWake, NewCapabilityFallbackWakeHandler, T211NewSessionRegistry и T211NewDeliveryRegistry вместе с тестами, которые их держат.
Контекст: Источник: docs/research/deadcode-audit.md, строки про AttachWake, capability fallback и T211-регистры. У AttachWake 25 вызовов только в tool_wake_test.go, ADR 0263 прямо говорит, что инструмент не регистрируется. Capability fallback упомянут в reachability.json:75 с устаревшей причиной. T211-блок занимает около 140 строк. Риск: средний, правка потянет много тестов.
Шаги: Сверить контракт из восьми инструментов. Удалить символы и их тесты. Обновить или удалить устаревшую строку reachability, пересобрав бинарник гейтов в том же PR, если конфиг изменится.
Проверка: tools/list contract test проходит без изменения золотых значений. CI зелёный.
Приёмка: Символов нет в продакшен-коде, число инструментов осталось восемь, причина в reachability не противоречит ADR 0263.

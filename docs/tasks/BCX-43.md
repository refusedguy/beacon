# BCX-43. Объявить поле reason в advertised схеме wait_result
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/schema.go, internal/infra/mcp/tool_wait_result.go, internal/infra/mcp/contract_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать расхождение между OutputSchema и фактическим ответом.
Контекст: Источник: docs/research/mcp-contract-drift-audit.md, таблица 4 строка 3. Фабрика схемы использует WaitResultOutput (schema.go:297), обработчик отдаёт WaitReasonOutput с полем reason (tool_wait_result.go:154-185). relaxSchema в contract_test.go:351-415 открывает структуру, поэтому тест проглатывает расхождение. Базовый замер и приоритет из отчёта: P1.
Шаги: Добавить reason в publicly объявленную схему или перестать отдавать его. Ограничить relaxSchema только явным списком полей. Добавить строгий тест ответа wait_result.
Проверка: CI зелёный. Строгий тест проверяет ответ по объявленной схеме. reason описан или отсутствует в обоих местах.
Приёмка: Клиент со строгой проверкой схемы не отвергает ответ wait_result.

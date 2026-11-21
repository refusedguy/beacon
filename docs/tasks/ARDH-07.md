# ARDH-07. Свести шесть таблиц MCP к одному дескриптору инструмента
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/mcp/server.go, internal/infra/mcp/schema.go, internal/infra/mcp/contract_test.go, internal/infra/mcp/schema_test.go
Зависимости: 
Ссылка: ADR 0094, ADR 0263
Агент: нет
Время: нет
baseline: контракт tools/list фиксирует 18212 байт и SHA-256 ba0a9f42031cab8ff4733f89a8f6b3d1424be695367cbb6f305a3a0b60865f99. Имена девяти инструментов повторялись в ToolNames, toolAnnotations, outputSchemaFactories, toolsListOrder, expectedToolNames и expectedToolKinds, плюс wantTools в schema_test.go.
Цель: Ввести дескриптор Name, Description, OutputType, Kind, Handler как источник для ToolNames, toolAnnotations, outputSchemaFactories и производных таблиц, сохранив Attach-хелперы и ручной золотой контракт.
Контекст: Источник: docs/research/go-metaprogramming.md, находка 1 и рекомендация 1. Восемь имён перечислены в шести таблицах, Tools/list занимает 16095 байт и зафиксирован SHA. Риск: средний. Полная авторегистрация по всем файлам с хендлерами нарушит ADR 0263 и откроет 13 инструментов вместо восьми.
Шаги: Описать дескриптор для восьми инструментов. Вывести из него только таблицы имён, аннотаций и фабрик схем. Оставить описания уникальным текстом, а contract_test и schema_test писать вручную.
Проверка: tools/list contract test проходит на тех же золотых значениях. CI зелёный.
Приёмка: Число инструментов осталось восемь, золотой контракт не обновлялся, дубли таблиц убраны.

## Подтверждение

PR #1167: https://github.com/refusedguy/beacon/pull/1167. Дескриптор `toolDescriptor` в internal/infra/mcp/server.go хранит Name, Description, OutputType, Kind и Handler для девяти инструментов. Из него выводятся ToolNames, toolAnnotations и outputSchemaFactories. Тесты contract_test.go и schema_test.go читают те же дескрипторы для имён, порядка и классов аннотаций. Attach-хелперы сохранены: каждый Handler вызывает тот же хелпер с теми же аргументами в том же порядке. Вывод tools/list не изменился, байт-счётчик 18212 и SHA-256 не тронуты. Добавлены TestToolDescriptorsDescribeAdvertisedTools и TestOutputSchemasComeFromDescriptors. gofmt применён к четырём Go-файлам, go test, go build и gates оставлены CI.

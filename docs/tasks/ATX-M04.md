# ATX-M04. Выводить схему beacon_wait_result из типа обработчика
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/schema.go, internal/infra/mcp/contract_test.go
Зависимости: 
Ссылка: docs/research/mcp-close-chain-gaps.md, finding M04
Агент: нет
Время: нет
Цель: Убрать расхождение между объявленной схемой вывода и тем, что реально возвращает `beacon_wait_result`.
Контекст: Источник: docs/research/mcp-close-chain-gaps.md, finding M04. Дескриптор строит схему из `WaitResultOutput` с пятью полями (`internal/infra/mcp/schema.go:52-59`). Обработчик возвращает `WaitReasonOutput` с полем `reason` (`internal/infra/mcp/tool_wait_result.go:162-175`). Контрактный тест открывает лишние поля через `relaxSchema` (`internal/infra/mcp/contract_test.go:381`), поэтому расхождение не видно. Baseline: 0 проверок строгой схемы для ответа с причиной ожидания.
Шаги: Сделать фабрику схемы для `beacon_wait_result` на основе `WaitReasonOutput`. Сохранить порядок пяти исходных полей, чтобы байтовый контракт не сломался случайно. Обновить золотой размер и SHA в том же PR. Добавить отдельный тест, который проверяет ответ по строгой схеме и видит поле `reason`. Оставить `relaxSchema` только для проверки обратной совместимости.
Проверка: Контрактный тест и тест строгой схемы зелёные в CI. Удаление поля `reason` из объявленной схемы ломает новый тест.
Приёмка: Advertised-схема описывает все поля ответа. Тест строгой схемы не зависит от `relaxSchema`.

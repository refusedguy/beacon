# BCX-66. Реализовать или пересмотреть ADR 0213 о версионированном вопросе
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: docs/adrs/0213-human-question-revision-row.md, docs/evals/w21-human-question-revision.md, internal/infra/mcp/tool_wake.go
Зависимости: 
Ссылка: ADR 0213
Агент: нет
Время: нет
Цель: Убрать обещание operation id и reply path, которых нет в main.
Контекст: Источник: docs/research/adr-consistency-audit.md, таблица строка 5 и раздел 0213. ADR предписывает версионированную строку, operation id, reply path и отказ при изменении вопроса. operationId встречается только в wake (tool_wake.go:342). Кода question_changed нет. Eval w21-human-question-revision.md описывает ожидаемое поведение без реализации. Базовый замер и приоритет из отчёта: P1.
Шаги: Либо реализовать operationId для question, reply path и question_changed с тестом на изменение текста, либо уточнить ADR и eval до фактического поведения. При реализации сверить с BCX-57.
Проверка: CI зелёный. Тест на изменение вопроса даёт question_changed или ADR явно не обещает его. Eval и код согласованы.
Приёмка: Документация и код одинаково описывают обработку изменения вопроса.

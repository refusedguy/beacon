# BCX-12. Собрать вердикты в один контракт и сделать app-зеркала производными
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/domain/types.go, internal/app/orchestrator/checkall.go, internal/app/orchestrator/orchestrator.go, internal/infra/mcp/schema.go, docs/GLOSSARY.md
Зависимости: 
Ссылка: ADR 0005
Агент: exe131
Время: 2026-11-21T18:04:56Z
Цель: Устранить расхождение семи наборов констант вердиктов и добавить недостающие значения в документы.
Контекст: Источник: docs/research/error-codes-audit.md, P0 «Вердикт живёт в семи наборах констант». domain/types.go не содержит fail, wait, wait_dependency, green_blocked. checkall.go:29-30 называет свой тип mirror. validVerdict в transition.go:442 не принимает blocked, needs_input, parked, accepted. green_blocked из verdict.go:883 отсутствует в GLOSSARY.md и README.md. Базовый замер и приоритет из отчёта: P0.
Шаги: Описать все вердикты и причины в одном файле контракта. Включить fail, wait, wait_dependency, green_blocked и все blocked/stop причины. Сделать app-зеркала производными или добавить тест равенства. Дописать green_blocked в GLOSSARY.md и README.md.
Проверка: CI зелёный. Тест сверяет доменный набор и зеркала. Документация перечисляет все вердикты, включая green_blocked.
Приёмка: Изменение доменного вердикта меняет поведение app через один источник. Ни одно значение не живёт только в зеркале.

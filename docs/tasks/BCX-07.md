# BCX-07. Сделать docs gate блокирующим после очистки exception таблицы
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: .github/workflows/docs.yml, tools/beacon-gates/gate/config/baseline.json, tools/beacon-gates/gate/config/permanent.json
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать continue-on-error у docs gate и свести baseline к нулю по устранённым причинам.
Контекст: Источник: docs/research/docs-health-audit.md, таблица 7 строка 7. continue-on-error: true, baseline 40 из 40, 35 DOC-ADR-DUP и 5 DOC-ADR-STRUCTURE. Gate проверяет структуру и дубли, но не ссылки, отсутствующие ADR и расхождение статусов с git. Базовый замер и приоритет из отчёта: P2.
Шаги: Сначала закрыть BCX-02, BCX-03 и BCX-05. Снять continue-on-error в workflow. Снижать baseline только после устранения корневых finding. Добавить rules DOC-PREMISE-STALE и разрешения локальных ссылок из BCX-01 и BCX-08.
Проверка: CI зелёный с обязательной джобой docs. Baseline содержит только актуальные, объяснённые исключения.
Приёмка: Красный docs gate блокирует мерж. Число записей в baseline меньше 40 и каждая имеет владельца и срок.

# BCX-68. Добавить команду test в каталог beacon
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/catalog.go, cmd/beacon/main.go, docs/adrs/0016-build-system.md
Зависимости: 
Ссылка: ADR 0016
Агент: нет
Время: нет
Цель: Сделать каталог полным по ADR 0016.
Контекст: Источник: docs/research/adr-consistency-audit.md, таблица строка 7. Dispatch содержит test в main.go:242, но catalog.go его не перечисляет. Агент читает каталог через beacon list --format json и не видит test. ADR называет каталог источником команд. Связано с BCX-21. Базовый замер и приоритет из отчёта: P2.
Шаги: Добавить test в catalog и ortho в генератор каталога из BCX-21. Добавить тест, который сверяет catalog и dispatch. Обновить docs/adrs/0016-build-system.md при изменении контракта.
Проверка: CI зелёный. beacon list --format json содержит test. Тест равенства каталога и dispatch проходит.
Приёмка: Каталог перечисляет все команды, включая test.

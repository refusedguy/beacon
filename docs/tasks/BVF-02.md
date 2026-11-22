# BVF-02. Заменить устаревшие owner_files в пакете bcx.json
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: docs/research/batches/bcx.json
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать десять ссылок на несуществующие файлы и две ссылки на каталоги в карточках BCX.
Контекст: Отчёт docs/research/BATCH-VALIDATION.md, раздел «Замечания по batches/bcx.json». Несуществующие файлы: docs/adrs/0091-mcp.md, tools/beacon-gates/gate/docsgate/docs.go, docs/adrs/0090-honest-delivery-unchanged-ladder.md, docs/GLOSSARY.md, internal/infra/server/http/error.go, docs/adrs/0086-mcp-beacon-wake.md, docs/adrs/0070-*.md, docs/research/process-leak-mechanism.md. Каталоги: docs/research/ и internal/infra/registry/.
Шаги: Для каждой ссылки найти актуальный путь в origin/main или заменить owner_file на реальный файл, который предстоит изменить. Шаблон docs/adrs/0070-*.md заменить конкретным файлом. Каталоги заменить на конкретные файлы внутри них. Оставить ссылки на генерируемые документы docs/FACTS.md и docs/adrs/README.md без изменения.
Проверка: Каждый owner_file в bcx.json существует в дереве origin/main, за исключением трёх генерируемых документов из .gitignore.
Приёмка: Проверка owner_files для bcx.json не возвращает ни одной ссылки на несуществующий файл или каталог.

# ATX-D03. Сделать research-источники проверяемыми
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: tools/beacon-gates/docsgate/docs.go, docs/research/docs-consistency-gaps.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать ссылки карточек на research-файлы, которых нет в git.
Контекст: Источник: docs/research/docs-consistency-gaps.md, находка ATX-D03. baseline: 19 отсутствующих файлов и 133 ссылки на них. Лидеры: `error-codes-audit.md` 12 ссылок, `mcp-contract-drift-audit.md` 11, `cli-ux-audit.md` 10, `e2e-coverage-audit.md` 10, `docs-health-audit.md` 10, `config-env-audit.md` 10, `adr-consistency-audit.md` 9. Часть файлов лежит в каталоге handoff как untracked, поэтому в `origin/main` их нет.
Шаги: Занести 19 путей в таблицу отчёта с решением по каждому. Добавить в docsgate проверку, что путь `docs/research/<name>.md`, который встречается в карточке, есть в дереве. Вынести на решение владельца выбор: закоммитить источники или заменить ссылки на существующий отчёт.
Проверка: CI зелёный. На дереве с искусственной ссылкой на отсутствующий research-файл gate выдаёт finding. Таблица отчёта заполнена для всех 19 путей.
Приёмка: У каждой ссылки есть существующий файл или явная пометка о замене. Дублей и пустых строк в таблице нет.

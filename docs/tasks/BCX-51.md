# BCX-51. Подключить офлайн-гейт tool-contract к CLI и CI
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: tools/beacon-gates/main.go, tools/beacon-gates/gate/tool_contract.go, .github/workflows/ci.yml
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Дать точке входа CheckToolContract вызываемую команду и место в CI.
Контекст: Источник: docs/research/mcp-contract-drift-audit.md, раздел 2.4 и главная находка 2. HEAD d3b1bfb1 добавил CheckToolContract и девять тестов. tools/beacon-gates/main.go:24-29 регистрирует только capabilities, layers-ast, layers-list, reachability. В .github, scripts, docs/gates и docs/runbooks упоминаний tool-contract нет. Фикстура в tool_contract_test.go:17-57 синтетическая, не реальный tools/list. Базовый замер и приоритет из отчёта: P1.
Шаги: Зарегистрировать подкоманду tool-contract в main.go. Добавить вызов в CI как обязательную или информационную проверку по решению владельца. Заменить синтетическую фикстуру реальным снимком инструментов. Проверить текущий main на наличие подкоманды.
Проверка: CI зелёный. beacon-gates tool-contract запускается и проверяет реальный снимок. В workflow есть вызов проверки.
Приёмка: Проверка качества описаний tools/list имеет точку входа и работает в CI.

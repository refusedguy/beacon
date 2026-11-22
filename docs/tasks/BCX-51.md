# BCX-51. Подключить офлайн-гейт tool-contract к CLI и CI
Статус: done
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

Итог: Already landed in PR #1315 (commit 93497d6a115fbbda3f191a45c1f2a0dad010c0c5). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1315 merged 2026-11-22T09:07:49+05:00; commit 93497d6a115fbbda3f191a45c1f2a0dad010c0c5; git merge-base --is-ancestor 93497d6a115fbbda3f191a45c1f2a0dad010c0c5 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1315 93497d6a
Время закрытия: 2026-11-22T11:24:28Z

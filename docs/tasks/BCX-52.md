# BCX-52. Свести три списка имён MCP к одному и вернуть e2e в обязательные
Статус: done
Фаза: P0
Тип: task
Владелец файлов: tests/e2e/e2e_test.go, internal/infra/mcp/server.go, evals/main.go, .github/workflows/ci.yml
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Предотвратить повторение падения 9 против 10 инструментов в CI.
Контекст: Источник: docs/research/e2e-coverage-audit.md, раздел 1 и таблица итог строка 1. expectedSet в tests/e2e, beaconmcp.ToolNames и evals/main.go три отдельных списка. Лог /tmp/opencode/e2e-1112.log:265 tools/list вернул 10 имён, :287 ожидалось девять, :294-304 два теста упали. Исправление f5c77e25 в ветке. eval в CI не запускается. Базовый замер и приоритет из отчёта: P0.
Шаги: Экспортировать набор имён из internal/infra/mcp и использовать в обоих е2е-вызовах и в evals. Удалить len(names) != 9 и числовые константы. Добавить unit-тест в mcp. Снять continue-on-error с джобы e2e после стабилизации. Исправить frozenScenarioHash и копию в tests/e2e/README.md одним коммитом.
Проверка: CI зелёный. Джоба e2e обязательна. Тест в internal/infra/mcp ловит новую регистрацию без правки контракта. Хеши в коде и README совпадают.
Приёмка: Регистрация инструмента без правки общего списка падает в обычной джобе test.

Итог: Already landed in PR #1322 (commit 50223cf0a878e5dd3182a875739568ca3e4f1642). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1322 merged 2026-11-22T09:01:16+05:00; commit 50223cf0a878e5dd3182a875739568ca3e4f1642; git merge-base --is-ancestor 50223cf0a878e5dd3182a875739568ca3e4f1642 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1322 50223cf0
Время закрытия: 2026-11-22T11:24:28Z

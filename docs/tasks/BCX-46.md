# BCX-46. Документировать одиннадцать добавочных полей get_result
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/schema.go, internal/infra/mcp/tool_get_result.go, docs/adrs/0094-seven-tools.md
Зависимости: 
Ссылка: ADR 0094
Агент: нет
Время: нет
Цель: Свести перечень полей ответа get_result с документацией контракта.
Контекст: Источник: docs/research/mcp-contract-drift-audit.md, таблица 4 строка 6. schema.go:115-138 определяет resultType, taskId, jobID, taskStatus, pollIntervalMs, instructions, nextAction, nextCommand, nextHint, failureExcerpt, configId. Большинство не перечислены в ADR 0094. Forward-compat зонд в contract_test.go:526-544 добавляет поля без объявления. Базовый замер и приоритет из отчёта: P1.
Шаги: Перечислить все поля в ADR или в версионированном приложении схемы. Разделить обязательные и forward-compatible поля. Заменить зонд, который добавляет поля без объявления, на проверку известного набора.
Проверка: CI зелёный. Тест сверяет набор полей ответа с документацией. Все одиннадцать полей имеют описание.
Приёмка: Клиент видит полный перечень полей и их назначение в контракте.

Итог: Already landed in PR #1299 (commit 4dafcf6912b41a07557f5e53a3f905d837fc4689). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1299 merged 2026-11-22T08:55:53+05:00; commit 4dafcf6912b41a07557f5e53a3f905d837fc4689; git merge-base --is-ancestor 4dafcf6912b41a07557f5e53a3f905d837fc4689 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1299 4dafcf69
Время закрытия: 2026-11-22T11:24:28Z

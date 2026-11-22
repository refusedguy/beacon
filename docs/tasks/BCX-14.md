# BCX-14. Дать BlockedInfo.Reason закрытый набор значений
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/domain/types.go, internal/infra/cibridge/bridge.go, internal/infra/mcp/tool_ack.go, cmd/beacon/up.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Заменить произвольную строку причины блокировки машиночитаемым кодом.
Контекст: Источник: docs/research/error-codes-audit.md, P1 «Причина блокировки не имеет закрытого набора». BlockedInfo.Reason в domain/types.go:191-192 это string. Пишут not_mine (tool_ack.go:643), предложение «failed checks already present on base branch» (cibridge/bridge.go:1096) и «not mine» в тестах. blockedReasonResolved = "resolved" (tool_ack.go:83) нигде не пишется. Базовый замер и приоритет из отчёта: P1.
Шаги: Ввести тип с закрытыми значениями, включая not_mine, resolved и причины из bridge. Убрать предложение из bridge.go:1096. Обновить потребителей up.go:407, inspect.go:194 и TUI. Привести тесты к продовым значениям.
Проверка: CI зелёный. Тесты проверяют все значения типа. Ни один потребитель не сравнивает причину с произвольной строкой.
Приёмка: BlockedInfo.Reason имеет тип и проходит проверку на записи. В логах и UI видны только известные коды.

Итог: Already landed in PR #1277 (commit 65cb659c63b7f73d3c3bf01c79a36c1a17c2e45f). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1277 merged 2026-11-22T09:23:42+05:00; commit 65cb659c63b7f73d3c3bf01c79a36c1a17c2e45f; git merge-base --is-ancestor 65cb659c63b7f73d3c3bf01c79a36c1a17c2e45f origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1277 65cb659c
Время закрытия: 2026-11-22T11:24:28Z

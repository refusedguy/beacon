# BCX-14. Дать BlockedInfo.Reason закрытый набор значений
Статус: todo
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

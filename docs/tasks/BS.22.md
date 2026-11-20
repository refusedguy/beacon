# BS.22. Audit log актора и ingress
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/store/audit.go
Зависимости: BS.12
Ссылка: ADR 0188
Агент: нет
Время: нет
Цель: Каждая привилегированная команда имеет автора.
Контекст: MCP, TUI и webhook имеют разные ingress.
Шаги: Таблица audit(actor, ingress, command_id, action), запись в транзакции.
Проверка: Мерж и wake имеют запись с актором.
Приёмка: Аудит доступен через /api/v1/audit.

## Подтверждение
- PREMISE проверен: функционал уже в origin/main, код не дублирован. PR #448 (commit 5245db2) влил `feat(store): audit log of actor and ingress (BS.22)`: `internal/infra/store/audit.go` задаёт таблицу `audit(actor, ingress, command_id, action, at)`, три ingress (`mcp`, `tui`, `webhook`), `validateAuditEntry` отвергает пустого актора, `RecordAuditTx` пишет в транзакции вызывающего (откат не оставляет строку), `ListAudit`/`AuditCount` читают лог. Приёмка закрыта в `internal/infra/server/api_audit.go` (`AuditPath = "/api/v1/audit"`). Статус done выставлен коммитом 5574278 (#533), дубль не создан.

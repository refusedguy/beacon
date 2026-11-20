# BS.20. Capability scope и каналы ухода
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/server.go
Зависимости: BS.14
Ссылка: ADR 0199
Агент: нет
Время: нет
Цель: Уход вызвавшего агента отменяет его дерево.
Контекст: CapabilityScopeRegistry уже хранит cancelled, sessionClosed, connectionBroken.
Шаги: Per-session scope, journal reasons, Depart по reason.
Проверка: Depart закрывает нужный канал и не трогает чужие.
Приёмка: go-sdk cancellation остаётся независимым select case.

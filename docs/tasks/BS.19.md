# BS.19. Долгий вызов как тикет с job id
Статус: done
Premise: проверено 2026-11-20, дубль. ADR 0094 фиксирует контракт ровно на восьми инструментах, девятый требует отдельного ADR. Тикет с job id и task dialect уже реализованы в main: ApplyTaskForm и поля JobID, ResultType, TaskID в GetResultOutput (internal/infra/mcp/schema.go), единый порог 30 с с потолком 120 с в syncCallConfig и передача его в Dependencies.After (cmd/beacon/mcp.go), переключение медленного ответа в task form (internal/infra/mcp/server.go). Это разрешено ADR 0031 §2 и ADR 0045 фича 2. Поле delivery из ADR 0026 не меняется. Новых инструментов и полей сверх восьми не требуется, дубль не создан.
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/server.go, cmd/beacon/mcp.go
Зависимости: BS.14
Ссылка: ADR 0191
Агент: нет
Время: нет
Цель: Медленный вызов не висит в соединении.
Контекст: Порог 30 с и потолок 120 с уже есть в ADR 0045.
Шаги: Переключать ответ в task dialect, возвращать ticket, ожидание по ticket.
Проверка: Вызов длиннее порога возвращает тикет, а не таймаут.
Приёмка: Поле delivery из ADR 0026 не меняется.

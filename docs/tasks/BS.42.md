# BS.42. MCP stdio как тонкий клиент сервера
Статус: done
Premise: проверено 2026-11-20. Число инструментов восемь верно для текущего main, но ссылка на ADR 0094 неверна. internal/infra/mcp/contract_test.go: expectedToolNames содержит восемь имён (семь из WORKER-PROMPT 4.1 плюс beacon_defer_wiring из W12-B), internal/infra/mcp/server.go: ToolNames тоже объявляет восемь. ADR 0094 (docs/adrs/0094-seven-tools.md) фиксирует ровно семь инструментов и прямо требует отдельного решения для восьмого; восьмой инструмент введён W12-B, а не ADR 0094. Сама карточка уже Статус: done с коммита 5574278, реализация в main: internal/infra/mcp/client.go: AttachRemoteTools подменяет локальные хендлеры тонкими HTTP-forward'ами на сервер, адрес берётся из connection.json, семь контрактных инструментов и CycleInstructions сохранены. Код не писал, OwnerFiles не менял, дубль не создан.
Фаза: P3
Тип: task
Владелец файлов: cmd/beacon/mcp.go, internal/infra/mcp/client.go
Зависимости: BS.6, BS.41
Ссылка: ADR 0189
Агент: нет
Время: нет
Цель: beacon mcp не владеет состоянием.
Контекст: Сейчас MCP поднимает CI-мост и store в том же процессе.
Шаги: Хендлеры через HTTP-клиент сервера, адрес из connection.json.
Проверка: При остановке сервера инструменты отвечают честной ошибкой.
Приёмка: Seven tools и CycleInstructions сохраняются.

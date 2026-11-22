# AUTOSP-03. Добавить Gemini в реестр ACP-раннеров
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: internal/infra/wake/acp.go, internal/infra/wake/acp_test.go
Зависимости: 
Ссылка: ADR 0306
Агент: нет
Время: нет
Цель: Запускать OpenCode, Claude, Codex, Pi и Gemini через один ACP-путь.
Контекст: Источник: docs/research/audit-01-autospawn.md, таблица строка 3. internal/infra/wake/acp.go:425-430 acpRuntimes содержит opencode, claude, codex и pi. :477 NewWorkerRuntime и :756 DeliverWake уже универсальны. baseline: rg -i gemini по internal/infra/wake и cmd/beacon дал 0 совпадений. Переопределение команд живёт в cmd/beacon/spawn.go:306-330 через BEACON_ACP_COMMAND и BEACON_ACP_ARGS.
Шаги: Добавить ключ gemini с командой запуска, соответствующей установленному бинарнику. Оставить override через переменные окружения без изменений. Обновить тест реестра и разрешённых имён. Не добавлять отдельный адаптер для Gemini.
Проверка: CI зелёный. SupportedACPRuntimes возвращает пять имён в стабильном порядке. Неизвестный раннер по-прежнему отвергается.
Приёмка: Один вызов --runtime acp --runner gemini поднимает раннер и ведёт сессию через WorkerRuntime.

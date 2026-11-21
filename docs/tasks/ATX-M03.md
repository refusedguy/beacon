# ATX-M03. Перенести cardID в контрактный вход beacon_watch_pr
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/tool_watch_pr.go, cmd/beacon/mcp.go, internal/infra/mcp/contract_test.go
Зависимости: 
Ссылка: docs/research/mcp-close-chain-gaps.md, finding M03
Агент: нет
Время: нет
Цель: Убрать расхождение между входной схемой `beacon_watch_pr` в контракте и схемой рабочего обработчика.
Контекст: Источник: docs/research/mcp-close-chain-gaps.md, finding M03. Дескриптор в `internal/infra/mcp/server.go:135-144` и тип `WatchPRInput` в `internal/infra/mcp/tool_watch_pr.go:192` не содержат `cardID`. Рабочий слой добавляет поле через `watchPRInputWithCard` в `cmd/beacon/mcp.go:522-525` и регистрирует обработчик с этим типом в `:565-572`. Baseline: 1 поле входа отличает `NewServer()` от production, golden читает только `NewServer()`.
Шаги: Добавить необязательное поле `cardID` в `mcp.WatchPRInput` с тем же описанием, что использует обёртка в `cmd`. Убрать локальный тип `watchPRInputWithCard` и регистрацию обработчика в `cmd`, перенеся связь карточки в отдельный хук или колбэк. Обновить золотые константы в том же PR. Проверить, что связь карточки с Watch создаётся как при явном `cardID`, так и через `claimedCardForNotify`.
Проверка: Контрактный тест и E2E зелёные в CI. Тест registration проверяет, что входная схема рабочего сервера содержит `cardID`.
Приёмка: Один тип входа для `beacon_watch_pr`. Поведение связи карточки не изменилось для вызовов с `cardID` и без него.

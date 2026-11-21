# ARDH-22. Убрать ссылку на глобальный срез в ответе MCP
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/mcp/server.go
Зависимости: 
Ссылка: https://github.com/refusedguy/beacon/pull/1191
Агент: нет
Время: нет
baseline: до правки `git grep -n 'SupportedProtocolVersions' -- internal/infra/mcp/server.go` находил ровно одну прямую передачу глобального среза `LegacyProtocolVersions` в ответ handshake. `git grep -n 'ToolNames' -- cmd/beacon internal/infra/mcp internal/infra/server` показывал чтение среза вне пакета в init, client и тестах контракта.
Цель: Вернуть копию LegacyProtocolVersions и ToolNames или сделать их приватными с функцией-геттером, чтобы ответ API не держал ссылку на глобальный срез.
Контекст: Источник: docs/research/fp-in-go-audit.md, раздел 2 и рекомендация 1. server.go:484 кладёт LegacyProtocolVersions прямо в SupportedProtocolVersions. Риск: низкий. Таблица констант сейчас не меняется, но вызывающий может изменить срез через ссылку.
Шаги: Сделать срезы приватными, добавить геттеры с копией. Обновить ToolNames, цикл регистрации и тесты контракта.
Проверка: tools/list contract test проходит на прежних золотых значениях. CI зелёный.
Приёмка: Ответ API содержит копию, изменение возвращённого среза не влияет на сервер.

## Подтверждение
- PR [#1191](https://github.com/refusedguy/beacon/pull/1191) добавил `LegacyProtocolVersionsCopy` и `ToolNamesCopy`. Каждый геттер возвращает свежую копию через `slices.Clone`. Ответ `initialize` использует копию версий. Новый тест меняет обе копии и проверяет, что глобальные срезы остались прежними.
- Экспортируемые переменные остались на месте. Перевод их в функции сломал бы вызывающих в `cmd/beacon/init.go`, `internal/infra/mcp/client.go` и трёх тестовых файлах за пределами OwnerFiles карточки. Контракт `tools/list`, имена инструментов и порядок не изменились.
- `gofmt` применён к `internal/infra/mcp/server.go` и новому тесту. Тесты, сборка и гейты оставлены CI по правилам репозитория.

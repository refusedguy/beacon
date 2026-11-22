# TUI-04. Добавить очередь, слоты и RSS в снимок проекции
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/httpapi/server.go, cmd/beacon/mcp.go
Зависимости: 
Ссылка: ADR 0306
Агент: нет
Время: нет
Цель: Отдать live-экрану данные очереди todo и deferred, ёмкости слотов и RSS без чтения файлов в слое cmd.
Контекст: Источник: docs/research/audit-03-tui.md, таблица строки 9, 10 и 11. internal/infra/httpapi/server.go:715 регистрирует маршруты, но маршрута очереди нет. cmd/beacon/board.go:48 загружает карточки через taskstore.LoadCards только в cmd. GLOSSARY.md:85 описывает слот. cmd/beacon/mcp.go:828 httpStateProvider.Snapshot уже заполняет снимок, а mcp.go:860 кладёт метрики в snap.Metrics. internal/infra/server/metrics.go:379 ProcessRSSBytes и metrics.go:440 metricsJSON.rssBytes даютRSS без нового сбора.
Шаги: Добавить необязательные поля queue, slots и workers в httpapi.Snapshot так, чтобы старый клиент не ломался. Заполнить их в httpStateProvider.Snapshot из уже доступных проекций store и реестра. Передать rssBytes из metricsJSON в клиент через существующее поле metrics. Не открывать SQLite и файлы из cmd.
Проверка: CI зелёный. Ответ /api/v1/snapshot содержит новые поля при наличии данных. Клиент без новых полей продолжает работать. RSS в ответе совпадает с VmRSS процесса.
Приёмка: Экран показывает очередь, слоты и RSS из одного ответа. Контракт расширен необязательными полями.

Итог: Already landed in PR #1593 (commit a16a28f34517e20d3e6b66d812f878e4cbea6c8a). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1593 merged 2026-11-22T12:15:05+05:00; commit a16a28f34517e20d3e6b66d812f878e4cbea6c8a; git merge-base --is-ancestor a16a28f34517e20d3e6b66d812f878e4cbea6c8a origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1593 a16a28f3
Время закрытия: 2026-11-22T11:25:31Z

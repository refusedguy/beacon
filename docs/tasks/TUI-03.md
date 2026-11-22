# TUI-03. Свести два HTTP-клиента к одному контракту
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/tui/client.go, cmd/beacon/watch.go
Зависимости: 
Ссылка: ADR 0306
Агент: нет
Время: нет
Цель: Убрать расхождение между TUI-клиентом и клиентом beacon watch --live и вернуть осмысленный хвост событий на холодном старте.
Контекст: Источник: docs/research/audit-03-tui.md, таблица строки 5, 6, 7 и 8. internal/tui/client.go:129 Snapshot ожидает pointStats.limit, а internal/infra/httpapi/server.go:71 PointStats содержит только used и budget. internal/infra/httpapi/client.go:177 ClientSnapshot использует другие имена полей. cmd/beacon/watch.go:499 watchHTTPPort уже реализует нужный порт. internal/tui/client.go:318 openEvents не передаёт ?replay, хотя server.go:951 replay умеет отдавать историю.
Шаги: Выбрать один клиент для обеих команд, а второй оставить как совместимый слой только если без него ломается watch --live. Заменить Points.Limit на budget в рендере либо заполнять limit в провайдере. Добавить role и phase в проекцию Subscriber из client.go:160. Передавать ?replay=N в openEvents и сохранить Last-Event-ID для переподключения. Не добавлять третий клиент.
Проверка: CI зелёный. Заголовок показывает бюджет без нуля. Хвост событий не пуст на первом кадре при включённом replay. Тесты client_test.go проходят на обоих форматах ответа.
Приёмка: Один декодер контракта на две команды. Поля points, subscribers и event tail совпадают с серверной проекцией.

Итог: Already landed in PR #1453 (commit 70f11147cacd980a33a56f709fdbcc2ea1ff1461). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1453 merged 2026-11-22T10:27:44+05:00; commit 70f11147cacd980a33a56f709fdbcc2ea1ff1461; git merge-base --is-ancestor 70f11147cacd980a33a56f709fdbcc2ea1ff1461 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1453 70f11147
Время закрытия: 2026-11-22T11:25:31Z

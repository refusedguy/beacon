# MEM-01. Добавить удаление сессии в HTTP-клиент opencode
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/opencode/client.go, internal/infra/opencode/client_test.go
Зависимости: AUTOSP-02
Ссылка: 
Агент: нет
Время: нет
Цель: Дать клиенту метод удаления сессии по закрытому Watch.
Контекст: Источник: docs/research/audit-02-memory.md, строка таблицы про сессии. internal/infra/opencode/client.go:342 содержит CreateSession, :370 содержит Prompt. Метода удаления нет: поиск DELETE /api/session по internal, cmd и .opencode дал только операции с in-memory картами. Baseline: ноль вызовов DELETE /api/session в продакшен-коде. Поля delivery и resume в Prompt уже назначены карточке AUTOSP-02 из audit-01, поэтому MEM-01 их не дублирует и зависит от AUTOSP-02. Адаптер в cmd/beacon/opencode.go:66 не входит в OwnerFiles этой карточки.
Шаги: Добавить DeleteSession(ctx, sessionID) с методом DELETE на путь /api/session/<id>, basic auth и ограничением тела ошибки через maxHTTPBodyBytes. Не менять сигнатуру Prompt: её расширяет AUTOSP-02. Покрыть тестами в client_test.go успешный DELETE, 404 и пустой sessionID.
Проверка: CI зелёный. В client.go есть DeleteSession и вызов DELETE. Тест проверяет путь и обработку 404. Вызовов go build и go test локально нет.
Приёмка: Сессия удаляется через HTTP. Изменения в Prompt не дублируют AUTOSP-02.

Итог: Already landed in PR #1462 (commit 9e5d3b91d0f9bb15ff090c711bae7beada07ae3f). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1462 merged 2026-11-22T10:01:00+05:00; commit 9e5d3b91d0f9bb15ff090c711bae7beada07ae3f; git merge-base --is-ancestor 9e5d3b91d0f9bb15ff090c711bae7beada07ae3f origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1462 9e5d3b91
Время закрытия: 2026-11-22T11:25:31Z

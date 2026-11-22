# MEM-01. Добавить удаление сессии в HTTP-клиент opencode
Статус: todo
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

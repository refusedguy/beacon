# TUI-05. Связать клавиши оператора с human-decide
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/tui/socket.go, cmd/beacon/up.go
Зависимости: 
Ссылка: ADR 0306
Агент: нет
Время: нет
Цель: Дать оператору клавиши approve, reject, context, rework, pause, resume, unblock и cancel с честным отображением результата.
Контекст: Источник: docs/research/audit-03-tui.md, раздел про горячие клавиши. internal/infra/httpapi/server.go:989 задаёт восемь разрешённых verbs. internal/tui/app.go:100 Command знает семь из них и псевдоним ack. internal/tui/client.go:237 Decide шлёт POST на /api/v1/human/decide. internal/tui/socket.go:42 содержит только ack, cancel, claim и release. cmd/beacon/up.go:89 upDecisionAdapter уже имеет резервный путь через store.
Шаги: Добавить verb context в Command. Назначить клавиши для восьми verbs, оставив r под refresh и используя F5 для обновления. Вызывать App.Decide и показывать Receipt вместе с причиной отказа. При желании расширить набор Command в socket.go до тех же verbs, не смешивая канал агентов с hotkeys оператора. Использовать уже существующий адаптер решений из up.go как образец резервного пути.
Проверка: CI зелёный. Каждая клавиша отправляет ожидаемый verb. Неизвестный verb не меняет экран. Отказ сервера виден в строке статуса. Тест проверяет все восемь verbs.
Приёмка: Оператор видит квитанцию и ошибку. Набор клавиш совпадает с контрактом сервера.

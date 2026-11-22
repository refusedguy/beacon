# AUTOSP-09. Явная связь cardID и watchID независимо от agentID
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/store/card_links.go, internal/infra/store/card_links_test.go
Зависимости: 
Ссылка: ADR 0306
Агент: нет
Время: нет
Цель: Связывать карточку с Watch без вывода cardID из идентификатора агента.
Контекст: Источник: docs/research/audit-01-autospawn.md, таблица строка 9. cmd/beacon/mcp.go:537 claimedCardForNotify ищет карточку по notify.Target как agentID. :593-602 вызывает db.Link. internal/infra/store/card_links.go:72-109 Link уже атомарен через INSERT OR IGNORE и одного writer. baseline: при headless-запуске runtime выдаёт sessionID, который не совпадает с agentID из card.
Шаги: Добавить метод, который принимает cardID и watchID из ответов claim и watch, сохраняет вызов Link и возвращает LinkResult. Оставить CardIDForAgent как запасной путь. Проверить непустые cardID и watchID. Обновить тесты card_links_test.go на гонку двух вызовов.
Проверка: CI зелёный. Два параллельных вызова дают одну строку. Пустые идентификаторы отвергаются.
Приёмка: При несовпадении agentID и sessionID карточка закрывается по связанному Watch.

# PROTO-02. Ввести типизированную SSE-обёртку для событий Watch
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/httpapi/server.go, internal/infra/httpapi/server_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Дать подписчикам /api/v1/events дискриминированное имя события и типизированную полезную нагрузку без смены транспорта.
Контекст: Источник: docs/research/audit-04-protocols.md, раздел б. Event в internal/infra/httpapi/server.go:298-304 несёт Type string и Payload json.RawMessage. writeSSE в :962-970 кодирует кадр. Реплей по Last-Event-ID живёт в :942-960. AG-UI использует типизированные события и пару StateSnapshot, StateDelta на JSON Patch RFC 6902.
Шаги: Добавить версию контракта по образцу HumanDecideContract в :60-63. Ввести закрытый набор имён событий, выведенный из Watch, Obligation и Verdict. Отдавать полный snapshot редко, между ним слать патчи. Сохранить heartbeat и реплей.
Проверка: CI зелёный. Тест на неизвестное имя события и на неверную форму полезной нагрузки. Тест на реплей по Last-Event-ID после патча.
Приёмка: Изменения только в OwnerFiles карточки. Существующий SSE и все команды /api/v1/ продолжают работать.

# BS.60. Channel ingress с opt-in
Статус: todo
Фаза: P5
Тип: task
Владелец файлов: internal/infra/channel/ingress.go
Зависимости: BS.59
Ссылка: ADR 0198
Агент: нет
Время: нет
Цель: Внешний канал не может писать в чат без разрешения.
Контекст: Crush требует --channels или channel_enabled и объявление capability.
Шаги: Capability check, opt-in, лимит размера, meta только идентификаторы, экранирование.
Проверка: Payload, который ломает <channel>, отбрасывается.
Приёмка: Server без opt-in молчит.

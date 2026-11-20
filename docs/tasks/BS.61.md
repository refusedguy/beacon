# BS.61. Детерминированный channel reply
Статус: dropped
Фаза: P5
Тип: task
Владелец файлов: internal/infra/channel/reply.go
Зависимости: BS.60
Ссылка: ADR 0198
Агент: нет
Время: нет
Цель: Ответ возвращается в канал, даже если модель не вызвала tool.
Контекст: Crush channel_reply имеет user, group и suppress_tools.
Шаги: Маршруты user/group, target_param, подавление дублей.
Проверка: Если модель уже ответила, автоматический reply пропущен.
Приёмка: Локальные turn не затрагиваются.

## Подтверждение
- PR #394 DROPPED: Deterministic channel reply (user/group/suppress_tools) has no channel layer in main; conflicts with the seven-tool MCP contract.

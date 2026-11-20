# W09_notifications_message_signal

## Сценарий

Beacon шлет ServerSession.Log с watchID, obligationID и revision в поле data; плагин слушает experimental.mcp.event.

## Проверки

- Уведомление приходит без подписки клиента и без активного запроса
- Плагин вызывает ctx.session.prompt с delivery steer
- Факт пробуждения session.execution.started, доставка текста session.inbox.delivered

## Негативный контроль

Если уровень логирования не задан, ServerSession.Log молча возвращает nil и сигнал не отправляется.

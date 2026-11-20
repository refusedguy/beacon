# W03_opencode_http_wake

## Сценарий

OpenCode HTTP wake. В GET /api/event?types=session.inbox.delivered,session.execution.started приходит событие с целевым sessionID.

## Проверки

- При delivery steer приходит session.execution.started с целевым sessionID
- Присутствует session.inbox.delivered
- Событие относится к нужной сессии

## Негативный контроль

Отправить delivery queue и отдельно resume:false. При двух других режимах execution.started нет.

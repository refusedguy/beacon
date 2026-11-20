# W06_acp_wake_sequence

## Сценарий

Beacon должен будить агента с нативной ACP-поддержкой, имея только сохраненный sessionId и текст ответа по обязательству.

## Проверки

- initialize согласует protocolVersion и читает capabilities
- До session/prompt вызывается session/resume при capability или session/load
- Агент начинает работу только после session/prompt

## Негативный контроль

Только initialize и session/load без session/prompt: агент подключается к сессии, но новую работу не начинает.

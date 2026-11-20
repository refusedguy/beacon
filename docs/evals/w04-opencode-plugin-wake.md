# W04_opencode_plugin_wake

## Сценарий

OpenCode плагин wake. prompt.ok delivery=steer в логе плагина плюс session.execution.started в SSE и в ctx.event.subscribe.

## Проверки

- В логе плагина есть prompt.ok delivery=steer
- session.execution.started приходит в SSE и в ctx.event.subscribe
- Квитанции прибавляются только при доставке

## Негативный контроль

Переименовать файл плагина. Без плагина нет ни prompt, ни события, квитанции не прибавляются.

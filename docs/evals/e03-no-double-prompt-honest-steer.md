# E03_no_double_prompt_honest_steer

## Сценарий

Сценарий ADR 0061 wake_delivery_honest_and_no_double_prompt включён в замороженный набор e2e. Проверяются отличия steer, queue и unsupported, отсутствие двойного prompt и негативный контроль на заглушке.

## Проверки

- steer, queue и unsupported различаются в логе плагина
- Повтор указателя с тем же id даёт EEXIST и не увеличивает число prompt
- negative_control_stub_handler_must_fail остаётся в наборе
- eval 02_catalog по-прежнему требует ровно семь инструментов

## Негативный контроль

Двойная запись события с одним id не должна порождать второй session.prompt. Если порождает, тест падает.

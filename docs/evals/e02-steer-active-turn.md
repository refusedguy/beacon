# E02_steer_active_turn

## Сценарий

ПоднятьStub executor с активным steerable turn и отправить follow в режиме steer.

## Проверки

- delivery равен steered
- Сообщение появилось в активном turn
- wake receipt записан

## Негативный контроль

Turn не steerable, ответ должен быть queued или typed denial.

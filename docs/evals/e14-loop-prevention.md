# E14_loop_prevention

## Сценарий

Seed late event, completion wake и pending question для одного вотча.

## Проверки

- один steer на блок
- терминальный статус стабилен
- snoozed не будится completion

## Негативный контроль

без blockedNotified и terminal guard агент будится в цикле

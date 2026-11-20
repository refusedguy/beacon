# W05_negative_control

## Сценарий

Проверка, что тест wake не проходит на холостом вызове и отличает запись ввода от планирования выполнения.

## Проверки

- Без steer нет session.execution.started
- На idle-сессии interrupt даёт interrupted false
- GET /api/session/active не содержит эту сессию

## Негативный контроль

prompt с resume=false: ввод записан в inbox, но execution не запланирован; wake наступает только после PATCH в steer.

# E05_wait_pending_question

## Сценарий

Запустить wait_result и поднять question от дочернего агента во время ожидания.

## Проверки

- wait вернулся с hasPendingQuestion true
- run остался активным
- Вопрос виден в родительском чате

## Негативный контроль

Вопрос отвечен до старта очевидного turn, turn отменяется.

## Продолжение той же Watch (BCX-57)

Сценарий `needs_input_continue_of_same_watch` из ADR 0061 пункт 4. Пока `wait_result` ждёт, дочерний агент поднимает вопрос и Watch уходит в `needs_input`. Ответ человека возвращается в ту же Watch. `wait_result` после ответа продолжает ждать ту же задачу и не создаёт новую.

### Шаги

1. `beacon_wait_result` ждёт Watch с ревизией 7 и возвращается с `hasPendingQuestion true`.
2. Человек отвечает `approve` через `beacon human` с тем же watchID.
3. Повторный `beacon_wait_result` читает ту же Watch и видит ревизию 8.

### Проверки

- watchID до ответа и после ответа одинаковый.
- Ревизия выросла на один, с 7 до 8.
- `beacon_list_running` показывает одну запись. Второй Watch нет.
- Обязательство не потеряно и имеет claimedAt.
- Агент просыпается в той же живой сессии.

### Негативный контроль

Вопрос изменён до ответа. Старый ответ отклонён. Watch остаётся в `needs_input`, вторая Watch не создаётся.

Автотест сценария: `TestNeedsInputContinueOfSameWatch` в `cmd/beacon/human_test.go`.

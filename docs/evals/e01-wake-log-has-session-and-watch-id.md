# E01_wake_log_has_session_and_watch_id

## Сценарий

Подписчик с sessionID ses_test получает retry wake от cibridge.

## Проверки

- в wake_delivered есть watchID и отдельный sessionID
- obligationID совпадает с ключом из watchID, revision и subscriberID
- в wake_delivered_total нет метки sessionID

## Негативный контроль

Старый путь observingDeliveryTarget без sessionID обязан упасть на проверке поля.

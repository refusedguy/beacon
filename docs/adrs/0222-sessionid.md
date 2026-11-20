# ADR 222: Единый sessionID сквозь логи, метрики и БД

Статус: proposed

## Контекст

В observe.go:46-68 нет поля sessionID. Все wake-события пишут watchID. observingDeliveryTarget.Prompt кладёт sessionID в аргумент watchID. По логу нельзя проследить агента и нельзя отличить orphan-сессию.

## Решение

Добавить поля sessionID, subscriberID, obligationID, reasonClass, traceID в observe и во все wake/ack-события. sessionID брать из subscriber.Target, а не из клиентского текста. Метрики оставить низкой кардинальности.

## Последствия

Лог становится связываемым с сессией OpenCode и обязательством. Растёт число полей в строках, нужен тест, что sessionID не попадает в метки метрик.

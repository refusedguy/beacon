# W01_bind_target

## Сценарий

Привязка цели. Адаптер получает указатель и ищет соответствие в SessionRegistry. Внешний факт: /api/plugin показывает адаптер active или RPC-Handshake Pi вернул get_state; в SessionRegistry есть строка watchID -> target.

## Проверки

- Адаптер находит target по watchID и subscriberID
- В SessionRegistry есть строка watchID -> target
- Привязка показывает active адаптер или get_state от Pi

## Негативный контроль

Убрать _meta и notify.target. Адаптер возвращает skip, никакого prompt и никакой квитанции.

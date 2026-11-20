# ADR 186: Стабильный адрес роли и поколение владельца

Статус: accepted

## Контекст

Seat в OpenRig — адрес вида member@rig, который переживает смену разговора; session-registry хранит generations и occupant tenure с kind initial/handover/adopt/fresh.

## Решение

Beacon разделяет адрес исполнителя, конкретную сессию и generations. Handoff меняет владельца строки, не меняя адрес роли.

## Последствия

История переходов остаётся chain of record; stale-владелец обнаруживается по generation, а не по имени сессии.

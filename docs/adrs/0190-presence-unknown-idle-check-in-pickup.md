# ADR 190: Presence честный: unknown вместо idle, check-in не равен pickup

Статус: accepted

## Контекст

Hosted member в OpenRig имеет lastCallAt в mcp-http-presence.json не чаще раза в минуту, lastWake среди 50 строк и checkIn с declared cadence. Статусы: checking-in, check-in-overdue, no-check-in-yet, on-request, disconnected, unknown. OpenRig явно не читает idle между вызовами.

## Решение

Завести такую presence-проекцию в Beacon: lastCallAt, lastWake, checkIn, declared cadence. Отсутствие данных это unknown, а не idle и не unavailable. Check-in пишется только при чтении собственного inbox.

## Последствия

Нельзя делать вид, что агент жив по факту зарегистрированного watch. standing-notice по unclaimed work приходит оператору, создателю или самому участнику после 60 минут по умолчанию.

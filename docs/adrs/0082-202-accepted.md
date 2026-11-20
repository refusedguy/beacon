# ADR 82: 202 Accepted: запрос не ждёт результата

Статус: accepted

## Контекст

На hosted Agent SDK каждый кастомный маршрут отвечает пустым 202 Accepted сразу после прибытия запроса и запускает handler фоном; возвращённое значение вызывающий не видит, результат идёт через events, callback или tool. Cursor steer отвечает 202 с delivery steered или queued. В Pi RPC disposition — started, queued, handled. Kilo session.prompt адмитит ввод и планирует wake, а resume:false даёт admit-only.

## Решение

Wake-вызов подтверждает приём, а не результат. beacon_ack фиксирует намерение и не ждёт вердикта. Вердикт приходит отдельным каналом events. Адаптер ведёт себя одинаково в hosted и локальном режиме: локальный serve в Cursor отличается от hosted тем, что возвращает результат handler.

## Последствия

Нельзя выводить готовность из факта приёма. FINISHED/IDLE у Cursor и agent_end у Pi не означают конец работы, Automations не помечают последний run. Завершает только явный verdict=ready. Cursor советует ещё 10 минут опрашивать latestRunId после последнего run как эвристику, но не как финальность.

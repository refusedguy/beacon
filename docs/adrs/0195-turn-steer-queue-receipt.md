# ADR 195: Доставка в активный turn: steer, queue и честный receipt

Статус: accepted

## Контекст

OpenCode session.prompt принимает delivery steer или queue; PATCH inbox меняет режим отложенной задачи; interrupt с resume=true оставляет queued prompts припаркованными. OpenRig добавляет typing guard и wake ladder с indeterminate вместо ложного delivered.

## Решение

Сопоставить режимы Beacon со значениями steer/queue: steer будит активный turn, queue кладёт в inbox, PATCH-аналог повышает задачу. Защита ручного ввода держит wake в UI, а не в терминале. Receipt фиксирует только факт принятия.

## Последствия

Серия повторов с тем же clientRequestId не шлёт сообщение дважды. Неизвестный результат wake читается как unknown. Сонная сессия просыпается только на pending question или failure.

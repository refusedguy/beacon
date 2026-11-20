# ADR 192: Контракт адаптера рантайма с типизированным провалом

Статус: accepted

## Контекст

RuntimeAdapter требует runtime/listInstalled/project/deliverStartup/launchHarness/checkReady; HarnessLaunchResult — union ok:true/ok:false с recovery и evidence.

## Решение

Beacon задаёт минимальный контракт адаптера исполнителя и требует типизированный отказ с подсказкой восстановления, без самоперезапуска.

## Последствия

Новый рантайм подключается без изменений ядра; неудачный resume не заменяется тихим fresh.

# ADR 189: Работа назначается очередью, сообщение только информирует

Статус: accepted

## Контекст

Правило OpenRig: rig send информирует, а rig queue create или handoff назначают работу; pickup это claim, а не чтение. Coordinator хранит 8 состояний queue_items и append-only queue_transitions. Beacon сейчас смешивает message и obligation внутри watch.

## Решение

Разделить inform и assign. Уведомление-указатель не меняет состояние обязательства. Назначение создаёт obligation с целевым адресом; executed или claim фиксирует pickup. Чтение статуса и list never меняют владельца.

## Последствия

Появляется явная модель transitions с audit. Оператор видит статус в проекциях, а не в истории чата. Требуется обратная совместимость с beacon_ack как claim-интентом.

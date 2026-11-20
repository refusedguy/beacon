# ADR 196: Fencing и идемпотентность на трёх уровнях

Статус: accepted

## Контекст

Beacon уже хранит (watch_id, revision), fencing_token и subscriber_id. OpenRig использует qitem id, outbox id и wake-intent-<qitem>; OpenCode имеет id сообщения и clientRequestId. Повтор после обрыва должен возвращать ту же работу.

## Решение

Один clientRequestId порождает command id, message id и wake receipt. Более старая revision или token отклоняется до записи. config_digest_mismatch трактуется как отказ регистрации.

## Последствия

Wake никогда не идёт за stale subscriber. Повторный ack не меняет вердикт. Нужны property-тесты на монотонность revision и token.

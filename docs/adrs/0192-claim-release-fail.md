# ADR 192: Рабочий пул с claim, release и fail вместо невидимой очереди

Статус: accepted

## Контекст

Cursor Cloud Agents API содержит register pool, watch pending pool requests, claim pending request, create session token, release claim и fail claim. Обычный follow-up при активном run получает 409 agent_busy, а steer отвечает steered с runId или queued без runId. Webhook automation использует Idempotency-Key и возвращает 422 при том же ключе и другом теле.

## Решение

Obligation в Beacon проходит через явный пул: создано, заявка, забрано воркером, освобождено, провалено с причиной. Busy-состояние всегда возвращает типизированный ответ вместо тихой постановки в очередь. Каждый входящий сигнал несёт ключ идемпотентности.

## Последствия

Зависший воркер не держит работу вечно. Повторный сигнал с тем же ключом и телом не создаёт вторую копию. Клиент видит, принята работа сразу или поставлена в очередь.

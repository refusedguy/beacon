# BS.25. Loopback webhook receiver с HMAC
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/webhook/receiver.go
Зависимости: BS.2
Ссылка: ADR 0193
Агент: нет
Время: нет
Цель: Приём GitHub webhook без публичного IP.
Контекст: GitHub рекомендует HMAC по сырому телу и constant-time сравнение.
Шаги: 127.0.0.1, crypto/hmac.Equal, лимит 25 MB, секрет 0600 рядом со state.
Проверка: Поддельная подпись даёт 401 без записи.
Приёмка: Подпись проверяется в Beacon, даже если туннель её проверил.

## Подтверждение
- PREMISE проверен: функционал уже в origin/main, код не дублирован. receiver.go: loopback 127.0.0.1 (LoopbackHost/loopbackWebhookAddr), gh.ValidatePayload проверяет HMAC-SHA256 по сырому телу с лимитом 25 MB, validationStatus даёт 401 на подписи до записи; signature.go: crypto/hmac + hmac.Equal (constant-time), LoadSecret требует 0600; receiver_test.go: wrong digest → 401 без Record. Подпись проверяется в Beacon.
Premise проверен 2026-11-20, дубль не создан. Функциональность уже в main: коммит 8c8cfc3 (PR #572) закрыл карточку как premise-дубль; receiver.go слушает только 127.0.0.1 через `LoopbackHost`/`loopbackWebhookAddr`, `gh.ValidatePayload` проверяет HMAC-SHA256 по сырому телу с лимитом 25 MB, `validationStatus` отдаёт 401 на подписи до записи; signature.go (`VerifySignature`) — crypto/hmac + `hmac.Equal` constant-time, `MaxBodyBytes` 25<<20, `LoadSecret` требует 0600; receiver_test.go `TestReceiverRejectsUnsignedDeliveries` (wrong digest → 401 без Record). Подпись проверяется в Beacon, даже если туннель её проверил. Код в OwnerFiles не писал.

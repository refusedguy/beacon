# BS.25. Loopback webhook receiver с HMAC
Статус: todo
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

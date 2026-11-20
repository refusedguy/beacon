# BS.21. Доменные ключи идемпотентности
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/domain/identity.go
Зависимости: BS.13
Ссылка: ADR 0196
Агент: нет
Время: нет
Цель: Один набор функций для трёх ключей.
Контекст: Есть domain.IdempotencyKey и obligation key вида watchID:revision:subscriber.
Шаги: Выделить ключи command, obligation и webhook delivery, экранирование.
Проверка: Одинаковые входы дают одинаковый ключ.
Приёмка: Ключи разных операций не делят слот.

# BCX-72. Задокументировать BEACON_CHANNEL_SENDERS и закрыть слэш-команды ingress
Статус: done
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/server.go, cmd/beacon/up.go, internal/infra/channel/ingress.go
Зависимости: 
Ссылка: 
Агент: exe50
Время: 2026-11-21T17:40:36Z
Цель: Сделать реестр отправителей видимым и действительно ограничивающим запись.
Контекст: Источник: docs/research/config-env-audit.md, итог строка 2. BEACON_CHANNELS и BEACON_CHANNEL_SENDERS читаются в server.go:33-86, применяются в channel/ingress.go:127-137. up.go:275-276 пропускает слэш-команды мимо ingress, поэтому BEACON_CHANNELS не ограничивает write-команды Telegram. Документации по SENDERS нет. Базовый замер и приоритет из отчёта: P0.
Шаги: Пропустить слэш-команды через ingress и проверить actor по SENDERS. Добавить описание формата sender=actor в README и runbook. Добавить тест, что write-команда от незарегистрированного actor отклоняется при включённом BEACON_CHANNELS.
Проверка: CI зелёный. Тест на незарегистрированного actor даёт отказ. Переменная описана в documentation и runbook.
Приёмка: Оператор может собрать реестр по документации, и канал действительно закрыт для чужих write-команд.

# BCX-80. Убрать env-переменные, которые production не читает
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/checkall_test.go, cmd/beacon/doctor.go, cmd/beacon/mcp.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать ложное ощущение, что BEACON_WAKE_TARGET и BEACON_GATES работают.
Контекст: Источник: docs/research/config-env-audit.md, итог строка 10. BEACON_WAKE_TARGET живёт только в checkall_test.go:85,120,158 и установлен в ses_ignored_env; настоящий wakeTarget приходит из Config через BEACON_CONFIG. BEACON_GATES читается только doctor.go:221 и не влияет на прод. Базовый замер и приоритет из отчёта: P2.
Шаги: Удалить тестовую переменную или переименовать её так, чтобы она не выглядела как production config. Переместить BEACON_GATES в раздел инструментов разработчика или удалить. Обновить центральную таблицу из BCX-75.
Проверка: CI зелёный. Поиск по коду не находит env, объявленных только в тестах, без пометки test-only. Таблица env отражает 실제 consumer.
Приёмка: Каждая переменная в документации имеет реального production-потребителя или помечена test-only.

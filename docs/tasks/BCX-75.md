# BCX-75. Описать 21 production-переменную в эксплуатационной документации
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: README.md, docs/runbooks/observability.md, docs/runbooks/verification.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Дать оператору возможность собрать рабочую конфигурацию по документации.
Контекст: Источник: docs/research/config-env-audit.md, итог строка 5 и раздел «Env в коде, но нет в документации». 21 production-переменная не названа в README или runbook, включая BEACON_CHANNEL_SENDERS, BEACON_TELEGRAM_TOKEN, BEACON_ROOT, BEACON_TASK_DEADLINE, BEACON_METRICS_PORT, таймауты MCP и BEACON_MCP_REMOTE. Центральной таблицы env нет. Базовый замер и приоритет из отчёта: P1.
Шаги: Создать центральную таблицу env с default, валидацией, consumer и риском. Перенести туда البيانات из cards и research. Добавить проверку, что каждая переменная из.cmd/internal/tools.has запись в таблице или разрешённое исключение.
Проверка: CI зелёный. Таблица содержит все production-переменные. Проверка не находит неописанных переменных.
Приёмка: Оператор находит любую рабочую переменную в одном документе.

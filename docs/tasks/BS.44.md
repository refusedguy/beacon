# BS.44. Совместимость CycleInstructions и dialect
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/mcp/server.go
Зависимости: BS.41
Ссылка: ADR 0189
Агент: нет
Время: нет
Цель: Цикл агента остался прежним.
Контекст: DialectFromRequest читает capabilities, а не версию протокола.
Шаги: Сохранить LegacyProtocolVersions, instructions и task dialect.
Проверка: Агент получает прежние шесть шагов цикла.
Приёмка: Новая логика не меняет вердикты.

## Подтверждение
- PR #391 MERGED: 2026-11-20 (squash).

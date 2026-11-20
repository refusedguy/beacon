# BS.8. Config resolver и env overlay
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/server/config.go
Зависимости: BS.5
Ссылка: ADR 0200
Агент: нет
Время: нет
Цель: Единая конфигурация для всех клиентов.
Контекст: Есть BEACON_STATE_DIR, BEACON_DAEMON_INTERVAL и проектные overlay.
Шаги: Глобальный конфиг, проектный overlay, env, отказ от fallback.
Проверка: Недоступный target не падает в другой проект.
Приёмка: Конфиг валидируется при старте.

## Подтверждение
- PR #388 MERGED: 2026-11-20 (squash).

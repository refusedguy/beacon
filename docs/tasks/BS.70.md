# BS.70. Архивация событий и квитанций
Статус: done
Фаза: P5
Тип: task
Владелец файлов: internal/infra/store/snapshots.go, internal/infra/store/receipts.go
Зависимости: BS.18
Ссылка: ADR 0190
Агент: нет
Время: нет
Цель: Журнал не растёт бесконечно.
Контекст: T3 держит около дня реплея, но receipts дольше.
Шаги: Retention join, сохранение high-water marks и prompt anchors.
Проверка: Старый курсор получает snapshot_required.
Приёмка: Повтор старой команды всё ещё дедуплицируется.

## Подтверждение
- PR #385 MERGED: 2026-11-20 (squash).

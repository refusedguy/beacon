# BS.18. Снимки и pruned-through sequence
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/store/snapshots.go
Зависимости: BS.11, BS.17
Ссылка: ADR 0190
Агент: нет
Время: нет
Цель: Старые события можно удалить без потери клиента.
Контекст: T3 хранит журнал около дня и сохраняет high-water marks.
Шаги: ProjectionMaintenance, snapshot по scope, событие snapshot_required.
Проверка: Курсор ниже pruned-through получает snapshot_required.
Приёмка: Квитанции живут дольше журнала.

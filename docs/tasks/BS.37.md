# BS.37. /api/v1 wake и вопросы
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/server/api_wake.go
Зависимости: BS.34, BS.22
Ссылка: ADR 0198
Агент: нет
Время: нет
Цель: TUI может отвечать на вопросы и инициировать wake.
Контекст: Crush имеет /permissions/grant, /questions/answer и /questions/cancel.
Шаги: Команды wake, question answer, approval respond с clientRequestId.
Проверка: Повтор команды возвращает прежний receipt.
Приёмка: Привилегированные действия пишут аудит.

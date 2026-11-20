# BS.58. Rebase pointer через daemon waker
Статус: todo
Фаза: P5
Тип: task
Владелец файлов: internal/app/orchestrator/daemon.go
Зависимости: BS.14
Ссылка: ADR 0202
Агент: нет
Время: нет
Цель: Rebase остаётся задачей исполнителя.
Контекст: DaemonWaker уже шлёт текст «нужен rebase, сделай его сам» с delivery=steer.
Шаги: Переиспользовать wake registry, не трогать ветку сервером.
Проверка: Один revision даёт один wake.
Приёмка: Неизвестный адаптер падает на opencode.

# BS.3. /healthz после готовности сервера
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: internal/infra/server/http.go, cmd/beacon/server.go
Зависимости: BS.2, BS.5
Ссылка: ADR 0195
Агент: нет
Время: нет
Цель: Health route сообщает о реальной готовности.
Контекст: Образцы: /api/config в opencode и /v1/health в Crush.
Шаги: Отвечать 200 только после БД, миграций и старта воркеров.
Проверка: До инициализации маршрут даёт 503.
Приёмка: TUI и CLI используют /healthz как precondition.

# BS.3. /healthz после готовности сервера
Статус: done
Premise: проверено 2026-11-20, функциональность уже в origin/main. Коммит 3ea54f8 (PR #621) добавил server.Readiness в internal/infra/server/http.go: Health() отвечает 503 на GET /healthz до MarkReady и 200 после, другие пути дают 404, не-GET дают 405. cmd/beacon/server.go объявляет serverReadiness и вызывает MarkReady в serverRunner после захвата лизы и разбора channel-конфигурации, перед запуском mcpRunner. Дубль не писал, OwnerFiles не менял.
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

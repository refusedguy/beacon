# ARDH-16. Упростить мелкие порты и убрать reflection в cibridge
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/app/session/delegation.go, internal/infra/server/ws.go, internal/domain/types.go, internal/infra/mcp/tool_wake.go, internal/infra/cibridge/bridge.go, cmd/beacon/watch_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Заменить порты с одним методом и парой адаптер-функция явными функциями, обращаться к Watch.FixFor напрямую и убрать лишний слой замыканий в тесте.
Контекст: Источник: docs/research/solid-gof-audit.md, раздел про Go без OOP, и docs/research/fp-in-go-audit.md, рекомендация 2. Notifier в session/delegation.go:220, ParentNotifier в server/ws.go:61, domain.Clock, reflection в bridge.go:1949, WakeSender и FallbackWakeSender, функция t3114AlivePID в watch_test.go:337. Риск: низкий. Интерфейс ACPTransport оставить, потому что у него две реализации.
Шаги: Заменить порты функциями там, где нет второй реализации. Убрать reflect.ValueOf и читать поле напрямую. Объединить WakeSender с явным параметром канала. Убрать функцию-обёртку в тесте.
Проверка: CI зелёный. Reflect в bridge.go для FixFor отсутствует.
Приёмка: Мелкие порты заменены функциями, Number инструментов и контракт домена не изменились.

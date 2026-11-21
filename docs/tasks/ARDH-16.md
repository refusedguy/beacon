# ARDH-16. Упростить мелкие порты и убрать reflection в cibridge
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/app/session/delegation.go, internal/infra/server/ws.go, internal/domain/types.go, internal/infra/mcp/tool_wake.go, internal/infra/cibridge/bridge.go, cmd/beacon/watch_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Заменить порты с одним методом и парой адаптер-функция явными функциями, обращаться к Watch.FixFor напрямую и убрать лишний слой замыканий в тесте.
Контекст: Источник: docs/research/solid-gof-audit.md, раздел про Go без OOP, и docs/research/fp-in-go-audit.md, рекомендация 2. Notifier в session/delegation.go:220, ParentNotifier в server/ws.go:61, domain.Clock, reflection в bridge.go:1949, WakeSender и FallbackWakeSender, функция t3114AlivePID в watch_test.go:337. Риск: низкий. Интерфейс ACPTransport оставить, потому что у него две реализации. После ARDH-11 reflection живёт в internal/infra/cibridge/verdict_applier.go, а не в bridge.go.
Шаги: Заменить порты функциями там, где нет второй реализации. Убрать reflect.ValueOf и читать поле напрямую. Объединить WakeSender с явным параметром канала. Убрать функцию-обёртку в тесте.
Проверка: CI зелёный. Reflect в bridge.go для FixFor отсутствует.
Приёмка: Мелкие порты заменены функциями, Number инструментов и контракт домена не изменились.

baseline: docs/research/solid-gof-audit.md, разделы «GOF-паттерны: лишние слои» и «Где Go идиоматичнее без OOP», и docs/research/fp-in-go-audit.md, раздел 4, дата 2026-11-21. Замер 2026-11-21 через rg по `internal/` и `cmd/`, код не запускался: 2 интерфейса с адаптер-функциями (session.Notifier/NotifierFunc и server.ParentNotifier/ParentNotifierFunc), 1 вызов reflect.ValueOf(w).FieldByName("FixFor") в cibridge, 1 фабрика t3114AlivePID с 4 вызовами, 2 порта WakeSender/FallbackWakeSender и 109 вызовов конструкторов wake-хендлеров в tool_wake_test.go. domain.Clock не менялся: 28 реализаций Now() и 303 вызова .Now() в 25 файлах. Критерий карточки «где нет второй реализации» для Clock не выполняется, и аудит прямо называет его общей лексикой ядра.

## Что сделано

session.Notifier и server.ParentNotifier стали именованными func-типами. Адаптеры NotifierFunc и ParentNotifierFunc и их методы Notify удалены. Вызовы в тестах передают метод Notify как значение функции. Nil-ветки в Ask сохранены.

watchFixFor читает domain.Watch.FixFor напрямую. Импорт reflect удалён из verdict_applier.go. Резервный путь через fixForSource сохранён, поведение не изменилось.

WakeSender и FallbackWakeSender объединены в один func-тип с явным параметром WakeChannel. Обычный путь передаёт пустой канал и сохраняет прежний выбор адаптера. Цепочка fallback передаёт steer или queue явно. Число инструментов MCP не изменилось.

Фабрика t3114AlivePID удалена. Четыре присваивания gcProcessAlive получили прямое замыкание.

## Подтверждение

PR: см. ссылку в следующем коммите.

## Решение по domain.Clock

Порт оставлен как интерфейс. Аудит даёт противоречивые рекомендации: заменить на func() time.Time и одновременно оставить как общую лексику ядра. Замер показывает 28 реализаций Now() и 303 вызова .Now(), то есть premise «много реализаций только в тестах» устарела. Перевод затронет 25 файлов без локальной проверки компиляции. Правка требует решения владельца и вынесена за пределы безопасной части карточки.

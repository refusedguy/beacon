# BCX-13. Решить судьбу кодов stop_uncertain и ACTOR_NOT_PERMITTED
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/registry_stop.go, internal/infra/httpapi/server.go, docs/tasks/T5.52.md, docs/tasks/T3.284.md, docs/adrs/0091-checked-stop.md
Зависимости: 
Ссылка: ADR 0091
Агент: нет
Время: нет
Цель: Прекратить обещание в документации кодов, которых нет в main.
Контекст: Источник: docs/research/error-codes-audit.md, P0 «Два кода из ADR реализованы в ветках». stop_uncertain со stopPhase описан в ADR 0091:15 и карточках T5.6, T5.52; коммит 13f8558c (#1107) не предок HEAD. ACTOR_NOT_PERMITTED из 29da86b6 тоже не предок; UNSUPPORTED_CONTRACT в main есть в httpapi/server.go:952. T5.52.md:4 и T3.284.md:2 всё ещё todo. Базовый замер и приоритет из отчёта: P0.
Шаги: Проверить текущий main на наличие обоих коммитов. Либо влить реализации и снять статус todo, либо явно пометить ADR и карточки как нереализованные с причиной. Добавить проверку, что код из ADR есть в main до публикации.
Проверка: CI зелёный. Статус карточки соответствует наличию кода на main. Проверка ADR не находит обещаний без реализации.
Приёмка: Документация не обещает stop_uncertain и ACTOR_NOT_PERMITTED, если их нет в main. Карточки имеют честный статус.

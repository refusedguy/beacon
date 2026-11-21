# ARDH-18. Сохранить причину ошибки через %w в 35 местах
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/agents.go, internal/infra/opencode/client.go, cmd/beacon/attach.go, internal/infra/wake/acp.go, internal/app/orchestrator/daemon.go, internal/app/event/bus.go, cmd/beacon/observe.go, internal/infra/wake/telegram.go
Зависимости: 
Ссылка: ADR 0009
Агент: нет
Время: нет
Цель: Заменить форматирование ошибки %v на %w там, где текст не очищается, и добавить sentinel ErrTelegramTransport для четырёх мест с sanitize.
Контекст: Источник: docs/research/error-handling-audit.md, находка 2 и рекомендация 2. Всего 39 мест: agents.go 16, opencode/client.go 10, telegram.go 4, attach.go 4, acp.go 2 и по одному в daemon.go, event/bus.go, observe.go. Риск: низкий. В telegram.go потеря цепочки это плата за удаление токена, поэтому очистку текста сохранить.
Шаги: Заменить 35 вызовов без очистки, оставив текст ошибки прежним. В четырёх местах telegram.go обернуть sentinel через %w и держать очищенный текст в сообщении. Обновить тесты errors.Is и errors.As.
Проверка: CI зелёный. Тесты на цепочку ошибок проходят для затронутых пакетов.
Приёмка: Цепочка cause сохраняется в 35 местах, токены в логах telegram не появляются.
